# === This file is part of ALICE O² ===
#
# Copyright 2018 CERN and copyright holders of ALICE O².
# Author: Teo Mrnjavac <teo.mrnjavac@cern.ch>
#
# This program is free software: you can redistribute it and/or modify
# it under the terms of the GNU General Public License as published by
# the Free Software Foundation, either version 3 of the License, or
# (at your option) any later version.
#
# This program is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU General Public License for more details.
#
# You should have received a copy of the GNU General Public License
# along with this program.  If not, see <http://www.gnu.org/licenses/>.
#
# In applying this license CERN does not waive the privileges and
# immunities granted to it by virtue of its status as an
# Intergovernmental Organization or submit itself to any jurisdiction.

cmake_minimum_required(VERSION 3.9)
cmake_policy(SET CMP0028 NEW)
cmake_policy(SET CMP0074 NEW)
cmake_policy(SET CMP0077 NEW)

### HACK
# lib is lib64 on CC7, but we want lib to be lib.
# It is probably bad to set this here: it should be system-provided
# or set in the aliBuild recipe.
set(CMAKE_INSTALL_LIBDIR lib)


###
### Product information
###

set(OCC_ORGANIZATION_NAME "O²")
set(OCC_ORGANIZATION_DOMAIN "github.com/AliceO2Group/Control")

set(OCCPLUGIN_PRODUCT_NAME "OCC Plugin")
set(OCCPLUGIN_PRODUCT_MAINTAINER "Teo Mrnjavac <teo.mrnjavac@cern.ch>")
set(OCCPLUGIN_DESCRIPTION_SUMMARY "The O² Configuration and Control plugin for FairMQ devices")

set(OCCLIB_PRODUCT_NAME "OCC Library")
set(OCCLIB_PRODUCT_MAINTAINER "Teo Mrnjavac <teo.mrnjavac@cern.ch>")
set(OCCLIB_DESCRIPTION_SUMMARY "The O² Configuration and Control library")

# Read version number from VERSION file in parent dir
set(OCC_VERSION_MAJOR 0)
set(OCC_VERSION_MINOR 0)
set(OCC_VERSION_PATCH 0)

set(VERSION_FILE "${CMAKE_SOURCE_DIR}/../VERSION")
file(READ "${VERSION_FILE}" VERSION_DATA)

string(REGEX MATCH "VERSION_MAJOR := ([0-9]*)" _ ${VERSION_DATA})
set(OCC_VERSION_MAJOR ${CMAKE_MATCH_1})
string(REGEX MATCH "VERSION_MINOR := ([0-9]*)" _ ${VERSION_DATA})
set(OCC_VERSION_MINOR ${CMAKE_MATCH_1})
string(REGEX MATCH "VERSION_PATCH := ([0-9]*)" _ ${VERSION_DATA})
set(OCC_VERSION_PATCH ${CMAKE_MATCH_1})

set(OCC_VERSION ${OCC_VERSION_MAJOR}.${OCC_VERSION_MINOR}.${OCC_VERSION_PATCH})
set(OCC_VERSION_SHORT ${OCC_VERSION})

set(CMAKE_MODULE_PATH
    ${CMAKE_MODULE_PATH}
    ${CMAKE_SOURCE_DIR}/cmake
    ${CMAKE_SOURCE_DIR}/cmake/modules)
include(FeatureSummary)
include(GenerateExportHeader)

message(STATUS "Build type: ${CMAKE_BUILD_TYPE}")
set(GIT_REPOSITORY_ROOT "${CMAKE_SOURCE_DIR}/..")
if (NOT (CMAKE_BUILD_TYPE STREQUAL Release) AND EXISTS "${GIT_REPOSITORY_ROOT}/.git/")
    include(CMakeDateStamp)
    set(OCC_VERSION_DATE "${CMAKE_DATESTAMP_YEAR}${CMAKE_DATESTAMP_MONTH}${CMAKE_DATESTAMP_DAY}")
    if (OCC_VERSION_DATE GREATER 0)
        set(OCC_VERSION ${OCC_VERSION}.${OCC_VERSION_DATE})
    endif ()

    include(CMakeVersionSource)
    if (CMAKE_VERSION_SOURCE)
        set(OCC_VERSION ${OCC_VERSION}-${CMAKE_VERSION_SOURCE})
    endif ()
endif ()
message(STATUS "OCC Library+Plugin version: ${OCC_VERSION}")


project(occ
    VERSION ${OCC_VERSION_SHORT}
    DESCRIPTION "${OCCLIB_DESCRIPTION_SUMMARY}"
    LANGUAGES CXX)

###
### Preamble: compiler stuff, CMake module paths
###
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

set(CMAKE_CXX_STANDARD 14)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_C_STANDARD 99)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF) #we want std=c++14, not std=gnu++14

if (CMAKE_COMPILER_IS_GNUCXX)
    if (CMAKE_CXX_COMPILER_VERSION VERSION_GREATER 4.9 OR
        CMAKE_CXX_COMPILER_VERSION VERSION_EQUAL 4.9)
        message(STATUS "Found GNU g++ ${CMAKE_CXX_COMPILER_VERSION}, enabling colorized error messages.")
        set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -fdiagnostics-color=auto")
    endif ()
endif ()

# Set the default build type to "RelWithDebInfo"
if(NOT CMAKE_BUILD_TYPE AND NOT CMAKE_CONFIGURATION_TYPES)
  set(CMAKE_BUILD_TYPE "RelWithDebInfo"
    CACHE
    STRING "Choose the type of build, options are: Debug Release RelWithDebInfo MinSizeRel Coverage."
    FORCE
  )
endif()

# Path options for dependencies
option(FAIRMQPATH "The path to the FairMQ install directory" /usr)
option(FAIRLOGGERPATH "The path to the FairLogger install directory" /usr)
option(PROTOBUFPATH "The path to the Protobuf install directory" /usr)
option(GRPCPATH "The path to the gRPC install directory" /usr)
option(BOOSTPATH "The path to the Boost install directory" /usr)

option(BUILD_EXAMPLES "Build examples" ON)

list(APPEND CMAKE_PREFIX_PATH
    ${FAIRMQPATH}
    ${FAIRLOGGERPATH}
    ${PROTOBUFPATH}
    ${GRPCPATH}
    ${BOOSTPATH})


###
### Find dependencies
###


list(APPEND BOOST_COMPONENTS program_options)

find_package(FairMQ 1.4.2 REQUIRED)
if (${FairMQ_FOUND})
    message(STATUS "Found FairMQ ${FairMQ_VERSION}")
    find_package(FairLogger ${FairMQ_FairLogger_VERSION} REQUIRED)
    if (${FairLogger_FOUND})
        message(STATUS "Found FairLogger ${FairLogger_VERSION}")
    endif()
    list(APPEND BOOST_COMPONENTS ${FairMQ_Boost_COMPONENTS})
endif()

find_package(Boost 1.68 REQUIRED COMPONENTS ${BOOST_COMPONENTS})

# Protobuf
set(protobuf_MODULE_COMPATIBLE TRUE)
find_package(protobuf 3.7.1 CONFIG REQUIRED)
message(STATUS "Using protobuf ${protobuf_VERSION}")

# OpenSSL on Mac
if (APPLE)
    find_package(OpenSSL REQUIRED)
endif()

# gRPC
find_package(gRPC 1.19.1 CONFIG REQUIRED)
message(STATUS "Using gRPC ${gRPC_VERSION}")

# gRPC C++ plugin
set(gRPC_CPP_PLUGIN_EXECUTABLE $<TARGET_FILE:gRPC::grpc_cpp_plugin>)

###
### Status messages for build options
###

if(${BUILD_EXAMPLES})
    message(STATUS "Code examples will be built (BUILD_EXAMPLES=ON)")
else()
    message(STATUS "Code examples will not be built (BUILD_EXAMPLES=OFF)")
endif()


###
### Protobuf + gRPC
###
set(PROTOFILES
    protos/occ.proto)

# NOTE: we want the files to end up in a subdirectory "protos" in the build dir,
#       but the Protobuf and gRPC generator functions don't support custom output
#       directories.
#       As a workaround, we rewrite CMAKE_CURRENT_BINARY_DIR for Protobuf/gRPC
#       and then restore it to its real CMake-managed value.
file(MAKE_DIRECTORY ${CMAKE_CURRENT_BINARY_DIR}/protos)
set(CMAKE_CURRENT_BINARY_DIR_OLD ${CMAKE_CURRENT_BINARY_DIR} )
set(CMAKE_CURRENT_BINARY_DIR ${CMAKE_CURRENT_BINARY_DIR}/protos)

# Protobuf+gRPC generator wrapper
function(PROTOBUF_GENERATE_GRPC_CPP SRCS HDRS)
    if (NOT ARGN)
        message(SEND_ERROR "Error: PROTOBUF_GENERATE_GRPC_CPP() called without any proto files")
        return()
    endif ()

    if (PROTOBUF_GENERATE_CPP_APPEND_PATH) # This variable is common for all types of output.
        # Create an include path for each file specified
        foreach (FIL ${ARGN})
            get_filename_component(ABS_FIL ${FIL} ABSOLUTE)
            get_filename_component(ABS_PATH ${ABS_FIL} PATH)
            list(FIND _protobuf_include_path ${ABS_PATH} _contains_already)
            if (${_contains_already} EQUAL -1)
                list(APPEND _protobuf_include_path -I ${ABS_PATH})
            endif ()
        endforeach ()
    else ()
        set(_protobuf_include_path -I ${CMAKE_CURRENT_SOURCE_DIR})
    endif ()

    if (DEFINED PROTOBUF_IMPORT_DIRS)
        foreach (DIR ${PROTOBUF_IMPORT_DIRS})
            get_filename_component(ABS_PATH ${DIR} ABSOLUTE)
            list(FIND _protobuf_include_path ${ABS_PATH} _contains_already)
            if (${_contains_already} EQUAL -1)
                list(APPEND _protobuf_include_path -I ${ABS_PATH})
            endif ()
        endforeach ()
    endif ()

    set(${SRCS})
    set(${HDRS})
    foreach (FIL ${ARGN})
        get_filename_component(ABS_FIL ${FIL} ABSOLUTE)
        get_filename_component(FIL_WE ${FIL} NAME_WE)

        list(APPEND ${SRCS} "${CMAKE_CURRENT_BINARY_DIR}/${FIL_WE}.pb.cc")
        list(APPEND ${HDRS} "${CMAKE_CURRENT_BINARY_DIR}/${FIL_WE}.pb.h")
        list(APPEND ${SRCS} "${CMAKE_CURRENT_BINARY_DIR}/${FIL_WE}.grpc.pb.cc")
        list(APPEND ${HDRS} "${CMAKE_CURRENT_BINARY_DIR}/${FIL_WE}.grpc.pb.h")

        # protoc cpp generator
        add_custom_command(
            OUTPUT "${CMAKE_CURRENT_BINARY_DIR}/${FIL_WE}.pb.cc"
            "${CMAKE_CURRENT_BINARY_DIR}/${FIL_WE}.pb.h"
            COMMAND protobuf::protoc
            ARGS --cpp_out  ${CMAKE_CURRENT_BINARY_DIR} ${_protobuf_include_path} ${ABS_FIL}
            DEPENDS ${ABS_FIL}
            COMMENT "Running C++ protocol buffer compiler on ${FIL}"
            VERBATIM)

        if(APPLE)
          set(libenv DYLD_LIBRARY_PATH=$<TARGET_FILE_DIR:gRPC::grpc++>:$<TARGET_FILE_DIR:protobuf::libprotobuf>)
        endif()
        # protoc grpc cpp generator
        add_custom_command(
            OUTPUT "${CMAKE_CURRENT_BINARY_DIR}/${FIL_WE}.grpc.pb.cc"
            "${CMAKE_CURRENT_BINARY_DIR}/${FIL_WE}.grpc.pb.h"
            COMMAND ${libenv} $<TARGET_FILE:protobuf::protoc>
            ARGS --grpc_out=${CMAKE_CURRENT_BINARY_DIR}
            --plugin=protoc-gen-grpc=${gRPC_CPP_PLUGIN_EXECUTABLE}
            ${_protobuf_include_path} ${ABS_FIL}
            DEPENDS ${ABS_FIL} protobuf::protoc
            COMMENT "Running gRPC C++ protocol buffer compiler on ${FIL}"
            VERBATIM)
    endforeach ()

    set_source_files_properties(${${SRCS}} ${${HDRS}} PROPERTIES GENERATED TRUE)
    set(${SRCS} ${${SRCS}} PARENT_SCOPE)
    set(${HDRS} ${${HDRS}} PARENT_SCOPE)
endfunction()

protobuf_generate_grpc_cpp(GRPC_SOURCES GRPC_HEADERS ${PROTOFILES})

set(CMAKE_CURRENT_BINARY_DIR ${CMAKE_CURRENT_BINARY_DIR_OLD})

###
### Build target OCCLIBRARY
###

set(LIBRARY_OUTPUT_PATH "${CMAKE_BINARY_DIR}/lib")
set(EXECUTABLE_OUTPUT_PATH "${CMAKE_BINARY_DIR}/bin")
set(INCLUDE_OUTPUT_DIRECTORY "${PROJECT_BINARY_DIR}/include/Occ")

set(OCCLIBRARY Occ)

configure_file(${CMAKE_CURRENT_SOURCE_DIR}/OccVersion.h.in
    ${CMAKE_CURRENT_BINARY_DIR}/OccVersion.h)

set(OCCLIBRARY_SOURCES
    occlib/OccInstance.cxx
    occlib/OccServer.cxx
    occlib/OccState.cxx
    occlib/RuntimeControlledObject.cxx)

set(OCCLIBRARY_PUBLIC_HEADERS
    occlib/OccInstance.h
    occlib/OccState.h
    occlib/RuntimeControlledObject.h
    OccGlobals.h)

add_library(${OCCLIBRARY} SHARED
    ${OCCLIBRARY_SOURCES}
    ${GRPC_SOURCES})

target_include_directories(${OCCLIBRARY}
    PUBLIC
    $<INSTALL_INTERFACE:include/Occ>
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}>
    $<BUILD_INTERFACE:${CMAKE_CURRENT_BINARY_DIR}>
    ${PROTO_SOURCE_DIR}
    ${GRPC_INCLUDE_DIRS})

target_link_libraries(${OCCLIBRARY}
    PUBLIC
    gRPC::grpc++
    protobuf::libprotobuf
    Boost::program_options)

generate_export_header(${OCCLIBRARY})

set_target_properties(${OCCLIBRARY} PROPERTIES
    CXX_VISIBILITY_PRESET hidden
    VERSION ${OCC_VERSION_SHORT}
    SOVERSION ${OCC_VERSION_SHORT}
    PUBLIC_HEADER "${OCCLIBRARY_PUBLIC_HEADERS}")

target_compile_features(${OCCLIBRARY} PUBLIC cxx_std_17)

# Install library
include(GNUInstallDirs)

# Build targets with install rpath on Mac to dramatically speed up installation
# https://gitlab.kitware.com/cmake/community/wikis/doc/cmake/RPATH-handling
set(CMAKE_INSTALL_RPATH_USE_LINK_PATH TRUE)
list(FIND CMAKE_PLATFORM_IMPLICIT_LINK_DIRECTORIES "${CMAKE_INSTALL_PREFIX}/lib" isSystemDir)
if(CMAKE_SYSTEM_NAME STREQUAL "Darwin")
  if("${isSystemDir}" STREQUAL "-1")
    set(CMAKE_INSTALL_RPATH "@loader_path/../lib")
  endif()
  set(CMAKE_BUILD_WITH_INSTALL_RPATH TRUE)
endif()
unset(isSystemDir)

install(TARGETS ${OCCLIBRARY}
    EXPORT OccTargets
    LIBRARY DESTINATION ${CMAKE_INSTALL_LIBDIR}
    ARCHIVE DESTINATION ${CMAKE_INSTALL_LIBDIR}
    PUBLIC_HEADER DESTINATION ${CMAKE_INSTALL_INCLUDEDIR}/Occ)

install(FILES "${CMAKE_CURRENT_BINARY_DIR}/occ_export.h"
    DESTINATION include/Occ)
install(FILES "${CMAKE_CURRENT_BINARY_DIR}/OccVersion.h"
    DESTINATION include/Occ)

include(CMakePackageConfigHelpers)

write_basic_package_version_file(
    "${CMAKE_CURRENT_BINARY_DIR}/cmake/OccConfigVersion.cmake"
    VERSION ${OCC_VERSION_SHORT}
    COMPATIBILITY AnyNewerVersion
)
install(EXPORT OccTargets
    FILE OccTargets.cmake
    NAMESPACE AliceO2::
    DESTINATION "${CMAKE_INSTALL_LIBDIR}/cmake/Occ"
)
configure_package_config_file(cmake/OccConfig.cmake.in cmake/OccConfig.cmake
    INSTALL_DESTINATION "${CMAKE_INSTALL_LIBDIR}/cmake/Occ"
    PATH_VARS CMAKE_INSTALL_PREFIX
)

install(FILES
    "${CMAKE_CURRENT_BINARY_DIR}/cmake/OccConfig.cmake"
    "${CMAKE_CURRENT_BINARY_DIR}/cmake/OccConfigVersion.cmake"
    DESTINATION ${CMAKE_INSTALL_LIBDIR}/cmake/Occ
)

###
### Build OCC example
###

if(${BUILD_EXAMPLES})
    add_subdirectory(occlib/examples)
endif()

###
### Build target OCCPLUGIN
###

set(OCCPLUGIN FairMQPlugin_OCC)

configure_file(${CMAKE_CURRENT_SOURCE_DIR}/OccVersion.h.in
               ${CMAKE_CURRENT_BINARY_DIR}/OccVersion.h)

set(OCCPLUGIN_SOURCES
    plugin/OccPlugin.cxx
    plugin/OccPluginServer.cxx)

add_library(${OCCPLUGIN} SHARED
    ${OCCPLUGIN_SOURCES}
    ${GRPC_SOURCES})

target_link_libraries(${OCCPLUGIN} PUBLIC
    FairMQ::FairMQ
    gRPC::grpc++
    protobuf::libprotobuf
    Boost::program_options)

target_include_directories(${OCCPLUGIN}
    PUBLIC
    ${CMAKE_CURRENT_SOURCE_DIR}
    ${CMAKE_CURRENT_BINARY_DIR}
    ${PROTO_SOURCE_DIR}
    ${FAIRROOT_INCLUDE_DIR}
    ${GRPC_INCLUDE_DIRS})

set_target_properties(${OCCPLUGIN} PROPERTIES
    CXX_VISIBILITY_PRESET hidden
    VERSION ${OCC_VERSION_SHORT}
    SOVERSION ${OCC_VERSION_SHORT})

install(TARGETS ${OCCPLUGIN} DESTINATION lib)

