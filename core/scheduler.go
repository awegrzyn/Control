/*
 * === This file is part of ALICE O² ===
 *
 * Copyright 2017-2018 CERN and copyright holders of ALICE O².
 * Author: Teo Mrnjavac <teo.mrnjavac@cern.ch>
 *
 * Portions from examples in <https://github.com/mesos/mesos-go>:
 *     Copyright 2013-2015, Mesosphere, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 * In applying this license CERN does not waive the privileges and
 * immunities granted to it by virtue of its status as an
 * Intergovernmental Organization or submit itself to any jurisdiction.
 */

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spf13/viper"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/AliceO2Group/Control/common/event"
	"github.com/AliceO2Group/Control/core/controlcommands"
	"github.com/AliceO2Group/Control/core/environment"
	"github.com/AliceO2Group/Control/core/task"
	"github.com/AliceO2Group/Control/core/task/constraint"
	"github.com/AliceO2Group/Control/executor/protos"
	"github.com/gogo/protobuf/proto"
	"github.com/mesos/mesos-go/api/v1/lib"
	"github.com/mesos/mesos-go/api/v1/lib/backoff"
	xmetrics "github.com/mesos/mesos-go/api/v1/lib/extras/metrics"
	"github.com/mesos/mesos-go/api/v1/lib/extras/scheduler/callrules"
	"github.com/mesos/mesos-go/api/v1/lib/extras/scheduler/controller"
	"github.com/mesos/mesos-go/api/v1/lib/extras/scheduler/eventrules"
	"github.com/mesos/mesos-go/api/v1/lib/extras/store"
	"github.com/mesos/mesos-go/api/v1/lib/resources"
	"github.com/mesos/mesos-go/api/v1/lib/scheduler"
	"github.com/mesos/mesos-go/api/v1/lib/scheduler/calls"
	"github.com/mesos/mesos-go/api/v1/lib/scheduler/events"
	"github.com/pborman/uuid"
	"github.com/sirupsen/logrus"
)

var (
	RegistrationMinBackoff = 1 * time.Second
	RegistrationMaxBackoff = 15 * time.Second
)

// StateError is returned when the system encounters an unresolvable state transition error and
// should likely exit.
type StateError string

func (err StateError) Error() string { return string(err) }

var schedEventsCh = make(chan scheduler.Event_Type)

func runSchedulerController(ctx context.Context,
							state *internalState,
							fidStore store.Singleton) error {
	// Set up communication from controller to state machine.
	go func() {
		for {
			receivedEvent := <-schedEventsCh
			switch {
			case receivedEvent == scheduler.Event_SUBSCRIBED:
				if state.sm.Is("INITIAL") {
					state.sm.Event("CONNECT")
				}
			}
		}
	}()

	// Set up communication from state machine to controller
	go func() {
		for {
			<- state.reviveOffersTrg
			doReviveOffers(ctx, state)
			state.reviveOffersTrg <- struct{}{}
		}
	}()

	// The controller starts here, it takes care of connecting to Mesos and subscribing
	// as well as resubscribing if the connection is dropped.
	// It also handles incoming events on the subscription connection.
	//
	// buildFrameworkInfo returns a *mesos.FrameworkInfo which includes the framework
	// ID, as well as additional information such as Roles, WebUI URL, etc.
	return controller.Run(
		ctx,
		buildFrameworkInfo(),
		state.cli, /* controller.Option...: */
		controller.WithEventHandler(buildEventHandler(state, fidStore)),
		controller.WithFrameworkID(store.GetIgnoreErrors(fidStore)),
		controller.WithRegistrationTokens(
			// Limit the rate of reregistration.
			// When the Done chan closes, the Run controller loop terminates. The
			// Done chan is closed by the context when its cancel func is called.
			backoff.Notifier(RegistrationMinBackoff, RegistrationMaxBackoff, ctx.Done()),
		),
		controller.WithSubscriptionTerminated(func(err error) {
			// Sets a handler that runs at the end of every subscription cycle.
			if err != nil {
				if err != io.EOF {
					log.WithPrefix("scheduler").WithField("error", err.Error()).
						Error("subscription terminated")
				}
				if _, ok := err.(StateError); ok {
					state.shutdown()
				}
				return
			}
			log.WithPrefix("scheduler").Info("disconnected")
		}),
	)
}


// buildEventHandler generates and returns a handler to process events received
// from the subscription. The handler is then passed as controller.Option to
// controller.Run.
func buildEventHandler(state *internalState, fidStore store.Singleton) events.Handler {
	// disable brief logs when verbose logs are enabled (there's no sense logging twice!)
	logger := controller.LogEvents(nil).Unless(viper.GetBool("verbose"))

	return eventrules.New( /* eventrules.Rule... */
		logAllEvents().If(viper.GetBool("verbose")),
		eventMetrics(state.metricsAPI, time.Now, viper.GetBool("summaryMetrics")),
		controller.LiftErrors().DropOnError(),
		eventrules.HandleF(notifyStateMachine(state)),
	).Handle(events.Handlers{
		// scheduler.Event_Type: events.Handler
		scheduler.Event_FAILURE: logger.HandleF(failure), // wrapper + print error
		scheduler.Event_OFFERS:  trackOffersReceived(state).HandleF(resourceOffers(state, fidStore)),
		scheduler.Event_UPDATE:  controller.AckStatusUpdates(state.cli).AndThen().HandleF(statusUpdate(state)),
		scheduler.Event_SUBSCRIBED: eventrules.New(
			logger,
			controller.TrackSubscription(fidStore, viper.GetDuration("mesosFailoverTimeout")),
		),
		scheduler.Event_MESSAGE: eventrules.HandleF(incomingMessageHandler(state, fidStore)),
	}.Otherwise(logger.HandleEvent))
}


// Channel the event type of the newly received event to an asynchronous dispatcher
// in runSchedulerController
func notifyStateMachine(state *internalState) events.HandlerFunc {
	return func(ctx context.Context, e *scheduler.Event) error {
		schedEventsCh <- e.GetType()
		return nil
	}
}

// Update metrics when we receive an offer
func trackOffersReceived(state *internalState) eventrules.Rule {
	return func(ctx context.Context, e *scheduler.Event, err error, chain eventrules.Chain) (context.Context, *scheduler.Event, error) {
		if err == nil {
			state.metricsAPI.offersReceived.Int(len(e.GetOffers().GetOffers()))
		}
		return chain(ctx, e, err)
	}
}

// Handle an incoming Event_FAILURE, which may be a failure in the executor or
// in the Mesos agent.
func failure(_ context.Context, e *scheduler.Event) error {
	var (
		f              = e.GetFailure()
		eid, aid, stat = f.ExecutorID, f.AgentID, f.Status
	)
	if eid != nil {
		// executor failed..
		fields := logrus.Fields{
			"executor":	eid.Value,
		}
		if aid != nil {
			fields["agent"] = aid.Value
		}
		if stat != nil {
			fields["error"] = strconv.Itoa(int(*stat))
		}
		log.WithPrefix("scheduler").WithFields(fields).Error("executor failed")
	} else if aid != nil {
		// agent failed..
		log.WithPrefix("scheduler").WithField("agent", aid.Value).Error("agent failed")
	}
	return nil
}

// Handler for Event_MESSAGE
func incomingMessageHandler(state *internalState, fidStore store.Singleton) events.HandlerFunc {
	// instantiate map of MCtargets, command IDs and timeouts here
	// what should happen
	// SendCommand sends a command, pushes the targets list, command id and timeout (maybe
	// through a channel) to a structure accessible here.
	// then when we receive a response, if its id, target and timeout is satisfied by one and
	// only one entry in the list, we signal back to commandqueue
	// otherwise, we log and ignore.
	return func(ctx context.Context, e *scheduler.Event) (err error) {
		log.Debug("scheduler.incomingMessageHandler BEGIN")
		defer log.Debug("scheduler.incomingMessageHandler END")

		mesosMessage := e.GetMessage()
		if mesosMessage == nil {
			err = errors.New("message handler got bad MESSAGE")
			log.WithPrefix("scheduler").
				WithError(err).
				Warning("message handler cannot continue")
			return
		}
		agentId := mesosMessage.GetAgentID()
		executorId := mesosMessage.GetExecutorID()
		if len(agentId.GetValue()) == 0 || len(executorId.GetValue()) == 0 {
			err = errors.New("message handler got MESSAGE with no valid sender")
			log.WithPrefix("scheduler").
				WithFields(logrus.Fields{
					"agentId": agentId.GetValue(),
					"executorId": executorId.GetValue(),
					"error": err.Error(),
				}).
				Warning("message handler cannot continue")
			return
		}

		data := mesosMessage.GetData()

		var incomingType struct {
			MessageType string `json:"_messageType"`
		}
		err = json.Unmarshal(data, &incomingType)
		if err != nil {
			return
		}

		switch incomingType.MessageType {
		case "DeviceEvent":
			var incomingEvent struct {
				Type pb.DeviceEventType        `json:"type"`
				Origin event.DeviceEventOrigin `json:"origin"`
			}
			err = json.Unmarshal(data, &incomingEvent)
			if err != nil {
				return
			}
			ev := event.NewDeviceEvent(incomingEvent.Origin, incomingEvent.Type)
			if ev != nil {
				handleDeviceEvent(state, ev)
			} else {
				log.WithFields(logrus.Fields{
						"type": incomingEvent.Type.String(),
						"originTask": incomingEvent.Origin.TaskId.Value,
					}).
					Error("cannot handle incoming device event")
			}

		case "MesosCommandResponse":
			var incomingCommand struct {
				CommandName string `json:"name"`
			}
			err = json.Unmarshal(data, &incomingCommand)
			if err != nil {
				return
			}

			log.WithPrefix("scheduler").WithField("commandName", incomingCommand.CommandName).Debug("processing incoming MESSAGE")
			switch incomingCommand.CommandName {
			case "MesosCommand_Transition":
				var res controlcommands.MesosCommandResponse_Transition
				err = json.Unmarshal(data, &res)
				if err != nil {
					log.WithPrefix("scheduler").WithFields(logrus.Fields{
						"commandName": incomingCommand.CommandName,
						"agentId":     agentId.GetValue(),
						"executorId":  executorId.GetValue(),
						"message":     string(data[:]),
						"error":       err.Error(),
					}).
						Error("cannot unmarshal incoming MESSAGE")
					return
				}
				sender := controlcommands.MesosCommandTarget{
					AgentId: agentId,
					ExecutorId: executorId,
					TaskId: mesos.TaskID{Value: res.TaskId},
				}

				go func() {
					state.taskman.UpdateTaskState(res.TaskId, res.CurrentState)
					state.servent.ProcessResponse(&res, sender)
				}()
				return
			default:
				return errors.New(fmt.Sprintf("unrecognized response for controlcommand %s", incomingCommand.CommandName))
			}
		}
		return
	}
}

func handleDeviceEvent(state *internalState, evt event.DeviceEvent) {
	if evt == nil {
		log.WithPrefix("scheduler").Error("cannot handle null DeviceEvent")
		return
	}

	switch evt.GetType() {
	case pb.DeviceEventType_END_OF_DATA:
		taskId := evt.GetOrigin().TaskId
		t := state.taskman.GetTask(taskId.Value)
		if t == nil {
			log.WithPrefix("scheduler").Error("cannot find task for DeviceEvent")
			return
		}
		env, err := state.environments.Environment(t.GetEnvironmentId().UUID())
		if err != nil {
			log.WithPrefix("scheduler").WithError(err).Error("cannot find environment for DeviceEvent")
		}
		if env.CurrentState() == "RUNNING" {
			err = env.TryTransition(environment.NewStopActivityTransition(state.taskman))
			if err != nil {
				log.WithPrefix("scheduler").WithError(err).Error("cannot stop run after END_OF_DATA event")
			}
		}
	}
}

// Handler for Event_OFFERS
func resourceOffers(state *internalState, fidStore store.Singleton) events.HandlerFunc {
	return func(ctx context.Context, e *scheduler.Event) error {
		var (
			offers                 = e.GetOffers().GetOffers()
			callOption             = calls.RefuseSeconds(time.Second)//calls.RefuseSecondsWithJitter(state.random, state.config.maxRefuseSeconds)
			tasksLaunchedThisCycle = 0
			offersDeclined         = 0
		)

		if viper.GetBool("veryVerbose") {
			var(
				prettyOffers []string
				offerIds []string
			)
			for i := range offers {
				prettyOffer, _ := json.MarshalIndent(offers[i], "", "\t")
				prettyOffers = append(prettyOffers, string(prettyOffer))
				offerIds = append(offerIds, offers[i].ID.Value)
			}
			log.WithPrefix("scheduler").WithFields(logrus.Fields{
				"offerIds":	strings.Join(offerIds, ", "),
				//"offers":	strings.Join(prettyOffers, "\n"),
			}).Debug("received offers")
		}

		var descriptorsToDeploy task.Descriptors
		select {
		case descriptorsToDeploy = <- state.tasksToDeploy:
			if viper.GetBool("veryVerbose") {
				rolePaths := make([]string, len(descriptorsToDeploy))
				taskClasses := make([]string, len(descriptorsToDeploy))
				for i, d := range descriptorsToDeploy {
					rolePaths[i] = d.TaskRole.GetPath()
					taskClasses[i] = d.TaskClassName
				}
				log.WithPrefix("scheduler").
					WithFields(logrus.Fields{
						"roles": strings.Join(rolePaths, ", "),
						"classes": strings.Join(taskClasses, ", "),
					}).
					Debug("received descriptors for tasks to deploy on this offers round")
			}
		default:
			if viper.GetBool("veryVerbose") {
				log.WithPrefix("scheduler").Debug("no roles need deployment")
			}
		}

		// by default we get ready to decline all offers
		offerIDsToDecline := make(map[mesos.OfferID]struct{}, len(offers))
		for i := range offers {
			offerIDsToDecline[offers[i].ID] = struct{}{}
		}

		tasksDeployed := make(task.DeploymentMap)

		if len(descriptorsToDeploy) > 0 {
			// 3 ways to make decisions
			// * FLP1, FLP2, ... , EPN1, EPN2, ... o2-roles as mesos attributes of an agent
			// * readout cards as resources
			// * o2 machine types (FLP, EPN) as mesos-roles so that other frameworks never get
			//   offers for stuff that doesn't belong to them i.e. readout cards

			// Walk through the roles list and find out if the current []offers satisfies
			// what we need.

			log.WithPrefix("scheduler").Debug("about to deploy workflow tasks")

			var	err error

			// We make a map[Descriptor]constraint.Constraints and for each descriptor to deploy we
			// fill it with the pre-computed total constraints for that Descriptor.
			descriptorConstraints := state.taskman.BuildDescriptorConstraints(descriptorsToDeploy)

			// NOTE: 1 offer per host
			for _, offer := range offers {
				var (
					remainingResources = mesos.Resources(offer.Resources)
					tasks = make([]mesos.TaskInfo, 0)
					tasksDeployedForCurrentOffer = make(task.DeploymentMap)
					targetExecutorId = mesos.ExecutorID{}
				)

				if len(offer.ExecutorIDs) == 0 {
					targetExecutorId.Value = uuid.NewUUID().String()
				} else {
					targetExecutorId.Value = offer.ExecutorIDs[0].Value
				}

				log.WithPrefix("scheduler").
					WithFields(logrus.Fields{
					"offerId":   offer.ID.Value,
					"resources": remainingResources.String(),
				}).Debug("processing offer")

				remainingResourcesFlattened := resources.Flatten(remainingResources)

				// avoid the expense of computing these if we can...
				if viper.GetBool("summaryMetrics") && viper.GetBool("mesosResourceTypeMetrics") {
					for name, resType := range resources.TypesOf(remainingResourcesFlattened...) {
						if resType == mesos.SCALAR {
							sum, _ := name.Sum(remainingResourcesFlattened...)
							state.metricsAPI.offeredResources(sum.GetScalar().GetValue(), name.String())
						}
					}
				}

				log.WithPrefix("scheduler").Debug("state lock")
				state.Lock()

				// We iterate down over the descriptors, and we remove them as we match
				FOR_DESCRIPTORS:
				for i := len(descriptorsToDeploy)-1; i >= 0; i-- {
					descriptor := descriptorsToDeploy[i]
					log.WithPrefix("scheduler").
						WithField("taskClass", descriptor.TaskClassName).
						Debug("processing descriptor")
					offerAttributes := constraint.Attributes(offer.Attributes)
					if !offerAttributes.Satisfy(descriptorConstraints[descriptor]) {
						if viper.GetBool("veryVerbose") {
							log.WithPrefix("scheduler").
								WithFields(logrus.Fields{
								    "taskClass": descriptor.TaskClassName,
								    "constraints": descriptorConstraints[descriptor],
								    "offerId": offer.ID.Value,
								    "resources": remainingResources.String(),
								    "attributes": offerAttributes.String(),
								}).
								Warn("descriptor constraints not satisfied by offer attributes")
						}
						continue
					}
					log.WithPrefix("scheduler").Debug("offer attributes satisfy constraints")

					wants := state.taskman.GetWantsForDescriptor(descriptor)
					if wants == nil {
						log.WithPrefix("scheduler").WithField("class", descriptor.TaskClassName).
							Warning("no resource demands for descriptor, invalid class perhaps?")
						continue
					}
					if !task.Resources(remainingResources).Satisfy(wants) {
						continue
					}

					// Point of no return, we start subtracting resources

					bindMap := make(map[string]uint64)
					for _, ch := range wants.BindPorts {
						availPorts, ok := resources.Ports(remainingResources...)
						if !ok {
							continue FOR_DESCRIPTORS
						}
						// TODO: this can be optimized by excluding the base range outside the loop
						availPorts = availPorts.Remove(mesos.Value_Range{Begin: 0, End: 8999})
						port := availPorts.Min()
						builder := resources.Build().
							Name(resources.Name("ports")).
							Ranges(resources.BuildRanges().Span(port, port).Ranges)
						remainingResources.Subtract(builder.Resource)
						bindMap[ch.Name] = port
					}

					agentForCache := task.AgentCacheInfo{
						AgentId: offer.AgentID,
						Attributes: offer.Attributes,
						Hostname: offer.Hostname,
					}
					state.taskman.AgentCache.Update(agentForCache) //thread safe

					taskPtr := state.taskman.NewTaskForMesosOffer(&offer, descriptor, bindMap, targetExecutorId)
					if taskPtr == nil {
						log.WithPrefix("scheduler").
							WithField("offerId", offer.ID.Value).
							Error("cannot get task for offer+descriptor, this should never happen")
						log.Debug("state unlock")
						continue
					}

					// Do not decline this offer
					_, contains := offerIDsToDecline[offer.ID]
					if contains {
						delete(offerIDsToDecline, offer.ID)
					}

					// Define the O² process to run as a mesos.CommandInfo, which we'll then JSON-serialize
					cmd := taskPtr.BuildTaskCommand()

					// Claim the control port
					availPorts, ok := resources.Ports(remainingResources...)
					if !ok {
						continue FOR_DESCRIPTORS
					}
					// The control port range starts at 47101
					// FIXME: make the control ports cutoff configurable
					availPorts = availPorts.Remove(mesos.Value_Range{Begin: 0, End: 29999})
					controlPort := availPorts.Min()
					builder := resources.Build().
						Name(resources.Name("ports")).
						Ranges(resources.BuildRanges().Span(controlPort, controlPort).Ranges)
					remainingResources.Subtract(builder.Resource)

					// Append control port to arguments
					// For the control port parameter and/or environment variable, see occ/OccGlobals.h
					cmd.Arguments = append(cmd.Arguments, "--control-port", strconv.FormatUint(controlPort, 10))
					cmd.ControlPort = controlPort
					cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", "OCC_CONTROL_PORT", controlPort))
					cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", "O2_ROLE", offer.Hostname))

					runCommand := *cmd

					// Serialize the actual command to be passed to the executor
					var jsonCommand []byte
					jsonCommand, err = json.Marshal(&runCommand)
					if err != nil {
						log.WithPrefix("scheduler").
							WithFields(logrus.Fields{
								"error": err.Error(),
								"value": *runCommand.Value,
								"args":  runCommand.Arguments,
								"shell": *runCommand.Shell,
								"json":  jsonCommand,
							}).
							Error("cannot serialize mesos.CommandInfo for executor")
						state.Unlock()
						log.WithPrefix("scheduler").Debug("state unlock")
						continue
					}

					// Build resources request
					resourcesRequest := make(mesos.Resources, 0)
					resourcesRequest.Add1(resources.NewCPUs(wants.Cpu).Resource)
					resourcesRequest.Add1(resources.NewMemory(wants.Memory).Resource)
					portsBuilder := resources.BuildRanges()
					for _, rng := range wants.StaticPorts {
						portsBuilder = portsBuilder.Span(rng.Begin, rng.End)
					}
					for _, port := range bindMap {
						portsBuilder = portsBuilder.Span(port, port)
					}
					portsBuilder = portsBuilder.Span(controlPort, controlPort)

					portRanges := portsBuilder.Ranges.Sort().Squash()
					portsResources := resources.Build().Name(resources.Name("ports")).Ranges(portRanges)
					resourcesRequest.Add1(portsResources.Resource)

					// Append executor resources to request
					executorResources := mesos.Resources(state.executor.Resources)
					log.WithPrefix("scheduler").
						WithField("taskResources", resourcesRequest).
						WithField("executorResources", executorResources).
						Debug("creating Mesos task")
					resourcesRequest.Add(executorResources...)

					newTaskId := taskPtr.GetTaskId()

					executor := state.executor
					executor.ExecutorID.Value = taskPtr.GetExecutorId()

					mesosTaskInfo := mesos.TaskInfo{
						Name:      taskPtr.GetName(),
						TaskID:    mesos.TaskID{Value: newTaskId},
						AgentID:   offer.AgentID,
						Executor:  executor,
						Resources: resourcesRequest,
						Data:      jsonCommand, // this ends up in LAUNCH for the executor
					}

					// We must run the executor with a special LD_LIBRARY_PATH because
					// its InfoLogger binding is built with GCC-Toolchain
					ldLibPath, ok := agentForCache.Attributes.Get("executor_env_LD_LIBRARY_PATH")
					mesosTaskInfo.Executor.Command.Environment = &mesos.Environment{}
					if ok {
						mesosTaskInfo.Executor.Command.Environment.Variables =
						append(mesosTaskInfo.Executor.Command.Environment.Variables,
							mesos.Environment_Variable{
								Name: "LD_LIBRARY_PATH",
								Value: proto.String(ldLibPath),
							})
					}

					log.WithPrefix("scheduler").
						WithFields(logrus.Fields{
						"taskId":     newTaskId,
						"offerId":    offer.ID.Value,
						"executorId": state.executor.ExecutorID.Value,
						"task":       mesosTaskInfo,
					}).Debug("launching task")

					tasks = append(tasks, mesosTaskInfo)
					descriptorsToDeploy = append(descriptorsToDeploy[:i], descriptorsToDeploy[i+1:]...)
					tasksDeployedForCurrentOffer[taskPtr] = descriptor
				}
				state.Unlock()
				log.WithPrefix("scheduler").Debug("state unlock")

				// build ACCEPT call to launch all of the tasks we've assembled
				accept := calls.Accept(
					calls.OfferOperations{calls.OpLaunch(tasks...)}.WithOffers(offer.ID),
				).With(callOption) // handles refuseSeconds etc.

				// send ACCEPT call to mesos
				err = calls.CallNoData(ctx, state.cli, accept)
				if err != nil {
					log.WithPrefix("scheduler").WithField("error", err.Error()).
						Error("failed to launch tasks")
					// FIXME: we probably need to react to a failed ACCEPT here
				} else {
					if n := len(tasks); n > 0 {
						tasksLaunchedThisCycle += n
						log.WithPrefix("scheduler").WithField("tasks", n).
							Info("tasks launched")
						for _, taskInfo := range tasks {
							log.WithPrefix("scheduler").
								WithFields(logrus.Fields{
									"executorId": taskInfo.GetExecutor().ExecutorID.Value,
									"executorName": taskInfo.GetExecutor().GetName(),
									"agentId": taskInfo.GetAgentID().Value,
									"taskId": taskInfo.GetTaskID().Value,
								}).
								Debug("launched")
						}

						// update deployment map
						for k, v := range tasksDeployedForCurrentOffer {
							tasksDeployed[k] = v
						}
					} else {
						offersDeclined++
					}
				}
			} // end for _, offerUsed := range offersUsed
		} // end if len(descriptorsToDeploy) > 0

		// build DECLINE call to reject offers we don't need any more
		declineSlice := make([]mesos.OfferID, len(offerIDsToDecline))
		j := 0
		for k := range offerIDsToDecline {
			declineSlice[j] = k
			j++
		}
		decline := calls.Decline(declineSlice...).With(callOption)

		if n := len(offerIDsToDecline); n > 0 {
			err := calls.CallNoData(ctx, state.cli, decline)
			if err != nil {
				log.WithPrefix("scheduler").WithField("error", err.Error()).
					Error("failed to decline tasks")
			} else {
				log.WithPrefix("scheduler").WithField("offers", n).
					Debug("offers declined")
			}
		} else {
			log.WithPrefix("scheduler").Info("no offers to decline")
		}

		// Notify listeners...
		select {
		case state.resourceOffersDone <- tasksDeployed:
			log.WithPrefix("scheduler").
				WithField("tasksDeployed", len(tasksDeployed)).
				Debug("notified listeners on resourceOffers done")
		default:
			if viper.GetBool("veryVerbose") {
				log.WithPrefix("scheduler").
					Debug("no listeners notified")
			}
		}

		// Update metrics...
		state.metricsAPI.offersDeclined.Int(offersDeclined)
		state.metricsAPI.tasksLaunched.Int(tasksLaunchedThisCycle)
		if viper.GetBool("summaryMetrics") {
			state.metricsAPI.launchesPerOfferCycle(float64(tasksLaunchedThisCycle))
		}
		var msg string
		if tasksLaunchedThisCycle == 0 {
			msg = "offers cycle complete, no tasks launched"
		} else {
			msg = "offers cycle complete, tasks launched"
		}
		log.WithPrefix("scheduler").WithField("tasks", tasksLaunchedThisCycle).Debug(msg)
		return nil
	}
}

// statusUpdate handles an incoming UPDATE event.
// This func runs after acknowledgement.
func statusUpdate(state *internalState) events.HandlerFunc {
	return func(ctx context.Context, e *scheduler.Event) error {
		s := e.GetUpdate().GetStatus()
		if viper.GetBool("verbose") {
			log.WithPrefix("scheduler").WithFields(logrus.Fields{
				"task":		s.TaskID.Value,
				"state":	s.GetState().String(),
				"message":	s.GetMessage(),
			}).Debug("task status update received")
		}

		// What's the new task state?
		switch st := s.GetState(); st {

		case mesos.TASK_FINISHED:
			log.WithPrefix("scheduler").Debug("state lock")
			state.Lock()
			state.tasksFinished++
			state.metricsAPI.tasksFinished()

			// FIXME: this should not quit when all tasks are done, but rather do some transition
			/*
			if state.tasksFinished == state.totalTasks {
				log.Println("Mission accomplished, all tasks completed. Terminating scheduler.")
				state.shutdown()
			} else {
				tryReviveOffers(ctx, state)
			}*/
			state.Unlock()
			log.WithPrefix("scheduler").Debug("state unlock")

		case mesos.TASK_LOST, mesos.TASK_KILLED, mesos.TASK_FAILED, mesos.TASK_ERROR:
			log.WithPrefix("scheduler").
				WithFields(logrus.Fields{
					"taskId": s.GetTaskID().Value,
					"state": st.String(),
					"reason": s.GetReason().String(),
					"source": s.GetSource().String(),
					"message": s.GetMessage(),
				}).
				Info("task inactive exception")
		}

		// Enqueue task state update
		go state.taskman.UpdateTaskStatus(&s)

		return nil
	}
}

// tryReviveOffers sends a REVIVE call to Mesos. With this we clear all filters we might previously
// have set through ACCEPT or DECLINE calls, in the hope that Mesos then sends us new resource offers.
// This should generally run when we have received a TASK_FINISHED for some tasks, and we have more
// tasks to run.
func tryReviveOffers(ctx context.Context, state *internalState) {
	// limit the rate at which we request offer revival
	select {
	case <-state.reviveTokens:
		// not done yet, revive offers!
		doReviveOffers(ctx, state)
	default:
		// noop
	}
}

func doReviveOffers(ctx context.Context, state *internalState) {
	err := calls.CallNoData(ctx, state.cli, calls.Revive())
	if err != nil {
		log.WithPrefix("scheduler").WithField("error", err.Error()).
			Error("failed to revive offers")
		return
	}
	log.WithPrefix("scheduler").Debug("revive offers done")
}

func KillTask(ctx context.Context, state *internalState, receiver controlcommands.MesosCommandTarget) (err error) {
	killCall := calls.Kill(receiver.TaskId.GetValue(), receiver.AgentId.GetValue())

	err = calls.CallNoData(ctx, state.cli, killCall)
	return
}

func SendCommand(ctx context.Context, state *internalState, command controlcommands.MesosCommand, receiver controlcommands.MesosCommandTarget) (err error) {
	log.Debug("SendCommand BEGIN")
	defer log.Debug("SendCommand END")
	var bytes []byte
	bytes, err = json.Marshal(command)
	if err != nil {
		return
	}

	message := calls.Message(receiver.AgentId.Value, receiver.ExecutorId.Value, bytes)

	err = calls.CallNoData(ctx, state.cli, message)

	log.WithPrefix("scheduler").
		WithFields(logrus.Fields{
		"agentId": receiver.AgentId.Value,
		"executorId": receiver.ExecutorId.Value,
		"payload": string(bytes),
		"error": func() string { if err == nil { return "nil" } else { return err.Error() } }(),
	}).
	Debug("outgoing MESSAGE call")

	return err
}

// logAllEvents logs every observed event; this is somewhat expensive to do so it only happens if
// the config is verbose.
func logAllEvents() eventrules.Rule {
	return func(ctx context.Context, e *scheduler.Event, err error, ch eventrules.Chain) (context.Context, *scheduler.Event, error) {
		log.WithPrefix("scheduler").WithField("event", fmt.Sprintf("%+v", *e)).
			Debug("incoming event")
		return ch(ctx, e, err)
	}
}

// eventMetrics logs metrics for every processed API event
func eventMetrics(metricsAPI *metricsAPI, clock func() time.Time, timingMetrics bool) eventrules.Rule {
	timed := metricsAPI.eventReceivedLatency
	if !timingMetrics {
		timed = nil
	}
	harness := xmetrics.NewHarness(metricsAPI.eventReceivedCount, metricsAPI.eventErrorCount, timed, clock)
	return eventrules.Metrics(harness, nil)
}

// callMetrics logs metrics for every outgoing Mesos call
func callMetrics(metricsAPI *metricsAPI, clock func() time.Time, timingMetrics bool) callrules.Rule {
	timed := metricsAPI.callLatency
	if !timingMetrics {
		timed = nil
	}
	harness := xmetrics.NewHarness(metricsAPI.callCount, metricsAPI.callErrorCount, timed, clock)
	return callrules.Metrics(harness, nil)
}

// logCalls logs a specific message string when a particular call-type is observed
func logCalls(messages map[scheduler.Call_Type]string) callrules.Rule {
	return func(ctx context.Context, c *scheduler.Call, r mesos.Response, err error, ch callrules.Chain) (context.Context, *scheduler.Call, mesos.Response, error) {
		if message, ok := messages[c.GetType()]; ok {
			log.WithPrefix("scheduler").Debug(message)
		}
		return ch(ctx, c, r, err)
	}
}
