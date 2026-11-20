# Pocket2s

Multiplayer Texas Hold'em in the browser. Players join a room by name, and the server runs the table (using [joker](https://github.com/alcamerone/joker)) and pushes state to every player over WebSockets.

**A note on AI use in this project**

The back end of this project was originally created as a server program that ran on a single EC2 instance. It has been refactored to take advantage of modern AWS capabilities, such as API Gateway, Lambda, and DynamoDB.

This refactor was done "by hand": all production Go and Terraform code was written out by a human, i.e. myself, and the system design decisions for the cloud environment are my own. Go tests were written with help from a coding agent.

## Contents

| Path              | What it is                                                                 |
|-------------------|----------------------------------------------------------------------------|
| `server/`         | Go backend: room/game logic, HTTP + WebSocket handlers, and a test CLI client |
| `landing/`        | React app: the landing page where players create or join a room            |
| `client/`         | React app: the game table UI                                               |
| `infrastructure/` | Terraform for the AWS deployment (API Gateway, Lambda, DynamoDB, IAM, budget alert) |

### Server layout (`server/`)

- `room/`, `messaging/`, `db/`, `cmap/`, `random/`, `http/rooms/`: shared packages (game room, message types and messengers, room stores, helpers, HTTP routes).
- `exec/server/`: standalone server using in-memory state. For local development.
- `exec/functions/http/` and `exec/functions/ws/`: AWS Lambda handlers (HTTP API and WebSocket API) backed by DynamoDB. This is what is deployed.
- `exec/client/`: interactive CLI client for playing against a server from a terminal.

### API

- `GET /room/check/{roomId}`: does the room exist?
- `POST /room/create/{roomId}`: create a room.
- `GET /room/connect/{roomId}/{playerId}`: upgrade to a WebSocket (local server only; in AWS the separate WebSocket API is used).
- `GET /healthcheck`: local server only.

## Prerequisites

- Go (version in `go.mod`)
- Node.js and npm
- Terraform and AWS credentials (deployment only)

## Server

### Run locally

```sh
ENVIRONMENT=local go run ./server/exec/server
```

This listens on `:2222` with in-memory state. A dev room called `pocket2s` is created at startup. Rooms with no connections are cleaned up after a few minutes. Without `ENVIRONMENT=local` the server tries to serve HTTPS using Let's Encrypt certificates for `api.pocket2s.com`, so always set it locally.

### Test

```sh
go test ./...
```

### Deploy

The deployed server is two AWS Lambda functions (HTTP and WebSocket handlers, `provided.al2023` / `arm64`) backed by DynamoDB. Terraform zips a binary named `bootstrap` from `infrastructure/lambda/http/` and `infrastructure/lambda/ws/` (both git-ignored), so build them first. From the `scripts/` directory (the script uses relative paths):

```sh
mkdir -p ../infrastructure/lambda/http ../infrastructure/lambda/ws
./build_lambda_binaries.sh
```

Then apply the infrastructure (see [Infrastructure](#infrastructure)). Terraform uploads the zips to S3 and updates the functions. Re-run the build script and `terraform apply` after any server change.

The Lambdas read their config from environment variables, which Terraform sets: `DDB_ROOM_STATE_TABLE`, `DDB_CONN_TO_ROOM_PLAYER_TABLE` and `AWS_APIGW_WS_ENDPOINT`.

### Using the CLI client

The CLI client is a terminal player for testing the server without the web UI. It is hard-coded to connect to `ws://localhost:2222`, so it only works against the local server.

1. Start the server (see above).
2. In one terminal per player, run:

   ```sh
   go run ./server/exec/client
   ```

3. Enter a player name, then a room name. Use `pocket2s` for the pre-created dev room.
4. When you see "Connection established", press **Enter** to mark yourself ready, or type `SIT OUT` to sit the first round out. The game starts once there are two or more players and everyone is ready.
5. When it is your turn, you are shown the table (dealer and blinds, community cards, pot, your cards and chips) and prompted with the valid actions. Type one (case-insensitive):

   | Command      | When                                                  |
   |--------------|-------------------------------------------------------|
   | `FOLD`       | any time                                              |
   | `CHECK`      | nothing owed                                          |
   | `CALL`       | something owed (the call cost is printed)             |
   | `BET <n>`    | nothing owed, e.g. `BET 40`                           |
   | `RAISE <n>`  | something owed, e.g. `RAISE 40`                       |
   | `ALLIN`      | you want to push all your chips                       |

6. After each hand the result is printed. Press **Enter** to ready up for the next round, or type `SIT OUT`. If you are out of chips, **Enter** buys you back in.

Other players' actions, joins and disconnects are printed as they happen. A player who disconnects sits out until they return. To try a full game alone, open two or more terminals.

## Landing page (`landing/`)

```sh
cd landing
npm install
npm start      # dev server
npm test       # tests (watch mode; CI=true npm test for a single run)
npm run build  # production build in landing/build
```

Configuration (environment variables, read at build time):

| Variable            | Default                  | Purpose                           |
|---------------------|--------------------------|-----------------------------------|
| `REACT_APP_API_URL` | `http://localhost:2222`  | Backend HTTP API                  |
| `REACT_APP_APP_URL` | `http://localhost:3001`  | Where the game client is served   |

To deploy, set those variables, run `npm run build` and publish `landing/build` to any static host.

## Game client (`client/`)

```sh
cd client
npm install
PORT=3001 npm start   # 3001 matches the landing page's default REACT_APP_APP_URL
npm test              # CI=true npm test for a single run
npm run build         # production build in client/build
```

Configuration:

| Variable              | Default                | Purpose                 |
|-----------------------|------------------------|-------------------------|
| `REACT_APP_HOST_URL`  | `ws://localhost:2222`  | Backend WebSocket URL   |

To deploy, set the variable (for production, the `wss://` URL of your WebSocket API), run `npm run build` and publish `client/build` to any static host.

### Running everything locally

1. `ENVIRONMENT=local go run ./server/exec/server`
2. `cd client && PORT=3001 npm start`
3. `cd landing && npm start` (it will pick the next free port if 3000 is taken)

## Infrastructure

Terraform in `infrastructure/` creates, in your AWS account:

- `dynamodb/`: room state and connection-to-room/player tables
- `lambda/`: S3 bucket for code, HTTP and WebSocket handler functions
- `apis/`: HTTP API and WebSocket API, each with a custom domain, ACM certificate and Route 53 record
- `iam/`: roles for the handlers
- a monthly AWS budget with email alerts at 90% and 100%

### Before you deploy

You need an AWS account, a Route 53 hosted zone for a domain you own, and Terraform with AWS credentials configured. The repo is set up for the author's deployment, so edit these first:

- `infrastructure/apis/apis.tf`: replace `api.pocket2s.com` and `ws.pocket2s.com` with hostnames in your zone, and `allow_origins` (`https://pocket2s.com`) with the origin(s) that serve your frontends.
- `infrastructure/lambda/lambda.tf`: the S3 bucket name `pocket2s-lambda-code-bucket` must be globally unique, so change it.
- `infrastructure/var.tfvars`: set `region` and `stage`.
- Terraform state is local and git-ignored. Keep it safe or configure a remote backend in `terraform.tf`.

### Deploy

Build the Lambda binaries first (see [Server → Deploy](#deploy)), then:

```sh
cd infrastructure
terraform init
terraform validate
terraform plan -var-file=var.tfvars \
  -var pocket2s_zone_id=<your-route53-zone-id> \
  -var budget_alert_email=<your-email> \
  -var monthly_budget_usd=<amount> -out=pocket2s.tfplan
terraform apply pocket2s.tfplan
```

The ACM certificates validate by DNS. If the first apply stalls on a certificate, add the validation CNAME records ACM shows for each one to your zone and re-apply.

Finally, build the frontends (see above) with `REACT_APP_API_URL` set to `https://<your API hostname>`, `REACT_APP_HOST_URL` set to `wss://<your WebSocket hostname>`, and `REACT_APP_APP_URL` set to where you host the game client.

## TODO

| Item | Done |
|------|------|
| Rewrite landing page to lighter VanillaJS page | ☐ | 
| Update front end to use React hooks | ☐ |
| Refresh front end visuals | ☐ |
| Add AI players | ☐ |

## Licence

See [LICENCE.md](LICENCE.md).
