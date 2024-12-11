# envoyproxy/ratelimit kubernetes controller

[![release](https://img.shields.io/github/release/DoodleScheduling/ratelimit-controller/all.svg)](https://github.com/DoodleScheduling/ratelimit-controller/releases)
[![release](https://github.com/doodlescheduling/ratelimit-controller/actions/workflows/release.yaml/badge.svg)](https://github.com/doodlescheduling/ratelimit-controller/actions/workflows/release.yaml)
[![report](https://goreportcard.com/badge/github.com/DoodleScheduling/ratelimit-controller)](https://goreportcard.com/report/github.com/DoodleScheduling/ratelimit-controller)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/DoodleScheduling/ratelimit-controller/badge)](https://api.securityscorecards.dev/projects/github.com/DoodleScheduling/ratelimit-controller)
[![Coverage Status](https://coveralls.io/repos/github/DoodleScheduling/ratelimit-controller/badge.svg?branch=master)](https://coveralls.io/github/DoodleScheduling/ratelimit-controller?branch=master)
[![license](https://img.shields.io/github/license/DoodleScheduling/ratelimit-controller.svg)](https://github.com/DoodleScheduling/ratelimit-controller/blob/master/LICENSE)

This controller manages deployments of [envoyproxy/ratelimit](https://github.com/envoyproxy/ratelimit) and its ruleset.
The controller can lookup `RateLimitRule` references and hook them up with a `RateLimitService`.
Each `RateLimitService` is a managed envoyproxy/ratelimit deployment which includes the related rules.

This approach is great for microservices which have their own ratelimits but a unified envoyproxy/ratelimit is needed.

## Example

```yaml
apiVersion: ratelimit.infra.doodle.com/v1beta1
kind: RateLimitService
metadata:
  name: default
spec:
  ruleSelector:
    matchLabels: {}
```

`matchLabels: {}` will include all of them in the same namespace as the service.
By using match labels or expressions it can be configured what rules should be included in the service.
If no `ruleSelector` on the service is configured no rules will be included.

Similar to the `ruleSelector` it is possible to match rules cross namespace by using `spec.namespaceSelector`. 
By default a `RateLimitService` only looks up rules from the same namespace but with a namespace selector this behaviour can be changed.
Using `namespaceSelector.matchLabels: {}` will lookup rules across all namespaces.

Following an example using one `RateLimitService` which connects to a redis-cluster and two ratelimit rules.

```yaml
apiVersion: ratelimit.infra.doodle.com/v1beta1
kind: RateLimitRule
metadata: 
  name: catchall-rule
spec:
  domain: apis.example.com
  unit: hour
  requestsPerUnit: 100
  shadowMode: true
  descriptors:
  - key: generic_key
  - key: authorization
---
apiVersion: ratelimit.infra.doodle.com/v1beta1
kind: RateLimitRule
metadata: 
  name: service-2
spec:
  domain: apis.example.com
  unit: hour
  requestsPerUnit: 100
  shadowMode: true
  replaces:
  - name: catchall-rule
  descriptors:
  - key: generic_key
    value: name=service-2
  - key: generic_key
  - key: authorization
---
apiVersion: ratelimit.infra.doodle.com/v1beta1
kind: RateLimitService
metadata: 
  name: ratelimit
spec:
  ruleSelector: {}
  deploymentTemplate:
    spec:
      template:
        spec:
          containers:
          - name: ratelimit
            resources:
              requests:
                memory: 128Mi
                cpu: 100m
              limits:
                memory: 128Mi
            env:
            - name: LOG_LEVEL
              value: debug
            - name: TRACING_EXPORTER_PROTOCOL
              value: grpc
            - name: TRACING_ENABLED
              value: "true"
            - name: OTEL_PROPAGATORS
              value: b3multi
            - name: OTEL_EXPORTER_OTLP_ENDPOINT
              value: http://opentelemetry-collector.tracing:4317
            - name: OTEL_SERVICE_NAME
              value: ratelimit
            - name: TRACING_SERVICE_NAME
              value: ratelimit
            - name: REDIS_PIPELINE_WINDOW
              value: 150us
            - name: LIMIT_RESPONSE_HEADERS_ENABLED
              value: "true"
            - name: USE_STATSD
              value: "true"
            - name: STATSD_HOST
              value: localhost
            - name: STATSD_PORT
              value: "9125"
            - name: LOG_FORMAT
              value: json
            - name: REDIS_SOCKET_TYPE
              value: tcp
            - name: REDIS_URL
              value: redis-cluster-ratelimit:6379
            - name: REDIS_TYPE
              value: cluster
            - name: REDIS_AUTH
              valueFrom:
                secretKeyRef:
                  key: auth
                  name: redis-ratelimit-auth
            - name: REDIS_TLS
              value: "false"
```

### Beta API notice
For v0.x releases and beta api we try not to break the API specs. However
in rare cases backports happen to fix major issues.

## Suspend/Resume reconciliation

The reconciliation can be paused by setting `spec.suspend` to `true`:
 
```
kubectl patch ratelimitservice default-p '{"spec":{"suspend": true}}' --type=merge
```

## Observe RateLimitService reconciliation

A `RateLimitService` will have all discovered resources populated in `.status.subResourceCatalog`.
Also there are two conditions which are useful for observing `Ready` and a temporary one named `Reconciling`
as long as a reconciliation is in progress.

## Installation

### Helm

Please see [chart/ratelimit-controller](https://github.com/DoodleScheduling/ratelimit-controller/tree/master/chart/ratelimit-controller) for the helm chart docs.

### Manifests/kustomize

Alternatively you may get the bundled manifests in each release to deploy it using kustomize or use them directly.

## Configuration
The controller can be configured using cmd args:
```
--concurrent int                            The number of concurrent RateLimitService reconciles. (default 4)
--enable-leader-election                    Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.
--graceful-shutdown-timeout duration        The duration given to the reconciler to finish before forcibly stopping. (default 10m0s)
--health-addr string                        The address the health endpoint binds to. (default ":9557")
--insecure-kubeconfig-exec                  Allow use of the user.exec section in kubeconfigs provided for remote apply.
--insecure-kubeconfig-tls                   Allow that kubeconfigs provided for remote apply can disable TLS verification.
--kube-api-burst int                        The maximum burst queries-per-second of requests sent to the Kubernetes API. (default 300)
--kube-api-qps float32                      The maximum queries-per-second of requests sent to the Kubernetes API. (default 50)
--leader-election-lease-duration duration   Interval at which non-leader candidates will wait to force acquire leadership (duration string). (default 35s)
--leader-election-release-on-cancel         Defines if the leader should step down voluntarily on controller manager shutdown. (default true)
--leader-election-renew-deadline duration   Duration that the leading controller manager will retry refreshing leadership before giving up (duration string). (default 30s)
--leader-election-retry-period duration     Duration the LeaderElector clients should wait between tries of actions (duration string). (default 5s)
--log-encoding string                       Log encoding format. Can be 'json' or 'console'. (default "json")
--log-level string                          Log verbosity level. Can be one of 'trace', 'debug', 'info', 'error'. (default "info")
--max-retry-delay duration                  The maximum amount of time for which an object being reconciled will have to wait before a retry. (default 15m0s)
--metrics-addr string                       The address the metric endpoint binds to. (default ":9556")
--min-retry-delay duration                  The minimum amount of time for which an object being reconciled will have to wait before a retry. (default 750ms)
--watch-all-namespaces                      Watch for resources in all namespaces, if set to false it will only watch the runtime namespace. (default true)
--watch-label-selector string               Watch for resources with matching labels e.g. 'sharding.fluxcd.io/shard=shard1'.
```
