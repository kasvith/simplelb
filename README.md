# SimpleLB

Simple LB is the simplest Load Balancer ever created.

It uses RoundRobin algorithm to send requests into set of backends and support
retries too.

It also performs active cleaning and passive recovery for unhealthy backends.

Since its simple it assume if / is reachable for any host its available
