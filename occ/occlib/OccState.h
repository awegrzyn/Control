/*
 * === This file is part of ALICE O² ===
 *
 * Copyright 2018 CERN and copyright holders of ALICE O².
 * Author: Teo Mrnjavac <teo.mrnjavac@cern.ch>
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

/**
 * @file OccState.h
 * @brief Types and helper functions related to OCC states.
 */
#ifndef OCC_OCCSTATE_H
#define OCC_OCCSTATE_H

#include "occ_export.h"

#include <string>
#include <unordered_map>


/**
 * States for the state machine used by RuntimeControlledObject.
 */
typedef enum {
    undefined,  /// Undefined state, this should never happen.
    standby,    /// Initial state for started or unconfigured processes.
    configured, /// Process configured and ready to perform data processing.
    running,    /// Data processing running, iterateRunning is continuously called in this state.
    paused,     /// Data processing temporarily on hold.
    error,      /// Generic error state, the machine is forced there when a transition or check fails.
    done        /// Final state of the process, it is not possible to go back from here, only quit.
} t_State;

t_State OCC_EXPORT getStateFromString(const std::string& s);
const std::string OCC_EXPORT getStringFromState(t_State s);

#endif //OCC_OCCSTATE_H
