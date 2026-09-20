#!/bin/bash

# This script compiles the proto files using buf.

# Exit immediately if a command exits with a non-zero status.
set -e

# Navigate to the api directory
cd api/
# update the buf dependencies
buf dep update
# Run buf generate from current directory
buf generate

echo "Proto files compiled successfully using buf."