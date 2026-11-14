#!/bin/sh
# podbox sets these at create time; defaults cover hand-run containers.
: "${HTTPS_PROXY:=http://proxy:3128}"
: "${HTTP_PROXY:=$HTTPS_PROXY}"
: "${NO_PROXY:=localhost,127.0.0.1}"
export HTTPS_PROXY HTTP_PROXY NO_PROXY
export https_proxy="${https_proxy:-$HTTPS_PROXY}" http_proxy="${http_proxy:-$HTTP_PROXY}" no_proxy="${no_proxy:-$NO_PROXY}"
if [ "$1" = podbox-idle ]; then
  # PID 1 keeper: exits on stop, reaps orphans of exec'd sessions.
  trap 'exit 0' TERM INT
  while :; do sleep 86400 & wait $!; done
fi
exec "$@"
