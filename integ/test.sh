#!/usr/bin/env bash

set -e

export NAMESPACE=default
export OPERATOR_IMAGE=lyft/flinkk8soperator:$(git rev-parse HEAD)
export INTEGRATION=true

# needed to create the checkpoints directory with world-writable permissions
umask 000

cd $(dirname "$0")
go test -timeout 20m -check.vv IntegSuite

