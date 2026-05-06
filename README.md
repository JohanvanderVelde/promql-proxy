# promql-proxy

Een reverse proxy die tussen Grafana en Mimir staat. De proxy bepaalt aan de hand van Kubernetes RBAC welke namespaces een gebruiker mag inzien en injecteert automatisch `namespace`-filters in alle PromQL queries via [prom-label-proxy](https://github.com/prometheus-community/prom-label-proxy).

## Architectuur

```
Grafana ──► promql-proxy ──► Mimir
                │
                └──► Kubernetes API (SubjectAccessReview + namespace list)
```

### Flow

1. Grafana stuurt een request met een HTTP header die de gebruiker identificeert (bijv. `X-Forwarded-User`, gezet door een auth proxy).
2. De proxy leest de gebruikersnaam en optioneel groepen (`X-Forwarded-Groups`).
3. Er wordt in de cache gekeken of de namespace-lijst voor deze gebruiker al bekend is.
4. Zo niet: via de Kubernetes Authorization API (`SubjectAccessReview`) wordt per namespace gecontroleerd of de gebruiker leesrechten heeft (standaard: `get` op `pods`).
5. Het resultaat wordt gecachet (standaard 60 seconden TTL).
6. De toegestane namespaces worden als filter geïnjecteerd in het PromQL request: `namespace=~"ns1|ns2|ns3"`.
7. Het aangepaste request wordt doorgestuurd naar Mimir.

## Configuratie

| Flag | Default | Beschrijving |
|------|---------|--------------|
| `--upstream` | *(verplicht)* | Upstream Mimir URL, bijv. `http://mimir-query-frontend:8080/prometheus` |
| `--listen-address` | `:8080` | Adres waarop de proxy luistert |
| `--metrics-address` | `:9090` | Adres voor `/metrics`, `/healthz` en `/ready` endpoints |
| `--header-name` | `X-Forwarded-User` | HTTP header met de gebruikersnaam |
| `--groups-header` | `X-Forwarded-Groups` | HTTP header met (komma-gescheiden) groepen |
| `--cache-ttl` | `60s` | TTL voor de RBAC namespace cache |
| `--rbac-resource` | `pods` | Kubernetes resource voor de RBAC check |
| `--rbac-api-group` | `""` | Kubernetes API group voor de RBAC check |
| `--rbac-verb` | `get` | Kubernetes verb voor de RBAC check |
| `--kubeconfig` | `""` | Pad naar kubeconfig (alleen voor lokale ontwikkeling) |

## Bouwen

```bash
go build -o promql-proxy .
```

Of met Docker:

```bash
docker build -t promql-proxy:latest .
```

## Deployen

### Vereiste RBAC

De ServiceAccount van de proxy heeft de volgende rechten nodig:

- `list` op `namespaces` (core API group)
- `create` op `subjectaccessreviews` (authorization.k8s.io)

```bash
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml
```

### Grafana Datasource

Configureer de Grafana Prometheus datasource om naar de proxy te wijzen:

```
URL: http://promql-proxy.monitoring.svc.cluster.local:8080
```

Zorg dat de gebruikersheader wordt doorgestuurd. Dit kan via:

1. **Auth proxy setup** — een OAuth2 Proxy of vergelijkbare oplossing vóór Grafana die `X-Forwarded-User` en `X-Forwarded-Groups` headers zet. Configureer Grafana met `[auth.proxy]` om deze headers te vertrouwen.
2. **Grafana datasource headers** — Configureer custom headers op de datasource met de juiste gebruikersinformatie.

## RBAC check

De proxy gebruikt `SubjectAccessReview` om te bepalen of een gebruiker toegang heeft tot een namespace. Standaard wordt gecontroleerd of de gebruiker `get` rechten heeft op `pods` in de betreffende namespace. Dit is dezelfde aanpak die OpenShift gebruikt: als je `kubectl top pod -n <namespace>` mag uitvoeren, mag je ook metrics voor die namespace zien.

Je kunt dit aanpassen met de `--rbac-resource`, `--rbac-api-group` en `--rbac-verb` flags.

## Caching

Namespace-lijsten worden per gebruiker gecachet met een configureerbare TTL (standaard 60 seconden). Dit voorkomt overbelasting van de Kubernetes API server. Verlopen entries worden automatisch opgeruimd.
