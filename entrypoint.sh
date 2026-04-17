#!/bin/sh
exec borg --host 0.0.0.0 --port "${PORT:-8000}" "$@"
