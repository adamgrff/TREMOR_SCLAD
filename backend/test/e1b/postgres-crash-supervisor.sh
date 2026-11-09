#!/bin/bash
# TEST only: keep the container/network namespace alive while the postmaster is
# SIGKILLed by the host controller. This is not a production restart policy.
child=''
trap 'if [ -n "$child" ]; then kill -TERM "$child" 2>/dev/null; wait "$child"; fi; exit 0' TERM INT
while true; do
  /usr/local/bin/docker-entrypoint.sh postgres -c listen_addresses=127.0.0.1 -c port=5432 &
  child=$!
  wait "$child"
  printf 'I4_TEST_POSTMASTER_EXIT restarting_after_crash\n'
  sleep 1
done
