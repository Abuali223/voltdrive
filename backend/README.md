# VoltDrive Backend

Brand-agnostic vehicle control API. Runs end-to-end today on an in-memory
mock fleet; real manufacturer APIs (Tesla, BYD, Voyah, ...) plug in as
adapters without changing the API layer or the app.

## Architecture

```
App ─HTTP/JSON─> api.Server ─> provider.Registry ─> VehicleProvider adapter
                     │                                 ├── mock   (works now)
                  auth (RBAC)                          ├── tesla  (Fleet API)
                                                       └── byd / voyah ...
```

- `internal/provider` — universal `VehicleProvider` interface + types
- `internal/provider/mock` — working simulated fleet (Voyah, Deepal)
- `internal/provider/tesla` — official Fleet API adapter (skeleton)
- `internal/auth` — identity + per-vehicle role permissions (RBAC)
- `internal/api` — HTTP routing, auth guard, command handlers

## Run locally

```bash
cd backend
go run ./cmd/server      # listens on :8080
```

Dev auth: send `Authorization: Bearer <uid>:<email>` (any value works in dev).

## Endpoints

| Method | Path | Action | Permission |
|--------|------|--------|-----------|
| GET  | /healthz | health check | none |
| GET  | /v1/vehicles/{id} | full snapshot | view |
| POST | /v1/vehicles/{id}/lock | lock doors | lock |
| POST | /v1/vehicles/{id}/unlock | unlock doors | lock |
| POST | /v1/vehicles/{id}/start | remote start | start |
| POST | /v1/vehicles/{id}/stop | remote stop | start |
| POST | /v1/vehicles/{id}/climate | `{"on":true,"targetC":24}` | climate |

Seeded vehicles: `voyah-001`, `deepal-002`.

## Deploy to Cloud Run

```bash
gcloud run deploy voltdrive-api \
  --source backend \
  --region europe-west1 \
  --min-instances 1 \
  --allow-unauthenticated
```

## Going to production

1. Replace `auth.DevVerifier` with a Firebase ID-token verifier.
2. Replace `auth.DevPermissions` with Firestore-backed roles.
3. Fill in `provider/tesla` (and add `provider/byd`, etc.) with real calls.
4. Bind real vehicles in `main.go` via `registry.Bind(vin, adapter)`.
5. Load brand secrets from Google Secret Manager.
