#!/bin/sh
set -e

# Determine which component to run based on the COMPONENT environment variable
if [ "$COMPONENT" = "eni-manager" ]; then
    echo "Starting ENI Manager..."
    exec /eni-manager "$@"
else
    echo "Starting ENI Controller..."
    exec /manager "$@"
fi
