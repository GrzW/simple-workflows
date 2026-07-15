# Simple Linear Workflow Engine

A lightweight, concurrent, and durable linear workflow execution engine written in Go.

---

## Architecture Decisions

### Monolithic Single Service
To keep deployment and reasoning straightforward, the engine runs as a single, self-contained service. Because workflows are purely linear and require no distributed coordination, splitting scheduling and execution into multiple services would introduce unnecessary network overhead and operational complexity.

### SQLite for Persistence
The engine uses a CGO-free SQLite driver (`modernc.org/sqlite`). This choice perfectly aligns with the requirements:
* **Zero-dependency deployment:** Simplifies compiling static binaries and constructing minimal Docker containers.
* **In-process speed:** Eliminates network round-trips; queries execute in microseconds, which is crucial since SQLite runs directly in the application process.
* **Concurrency-safe configuration:** Optimized with Write-Ahead Logging (WAL), `busy_timeout(5000)`, and a connection pool of 25 connections to enable concurrent reads while relying on SQLite's built-in write serialization.

---

## Durability & Concurrency

### Bounded Concurrency
The engine controls resource consumption using a configurable, bounded worker pool.
* Jobs are fed through a buffered channel of size `128`.
* If the engine's ingestion queue is full under heavy load, new submission requests fail gracefully with an HTTP `503 Service Unavailable` response, ensuring backpressure.

### Crash Recovery & Fault Tolerance
* **Idempotency (No Recomputation):** Completed tasks are never re-run. During a workflow execution, the engine evaluates the status of each task; if it is already marked `Completed` in the database, it is skipped, and its cached output is used.
* **At-Least-Once Recovery for In-Flight Tasks:** If the engine crashes while a task is running, the workflow status remains `Running`. On startup, the system scans the database for any incomplete (`Pending` or `Running`) workflows created **before the current application startup time** and re-submits them to the worker queue. Restricting recovery to pre-startup workflows avoids recovery races and duplication of newly submitted workflows. The in-flight task will safely restart from the beginning.
* **Graceful Shutdown:** The engine listens for `SIGTERM` signals. When stopping, it stops accepting new HTTP requests and drains active workers, giving them up to `30 seconds` to finish executing their current tasks.

---

## Workflow Input & Reference Syntax

Tasks reference input values or outputs from preceding tasks using a standardized, predictable double-brace template format:

| Context | Reference Syntax | Example |
| :--- | :--- | :--- |
| **Workflow Input** | `$.input.<field_name>` | `{{ $.input.a }}` |
| **Task Outputs** | `$.steps.<task_position>` | `{{ $.steps.0 }}` *(0-indexed position)* |

### Handling Undefined References
* **Text Templates (`Print` tasks):** Missing or undefined references gracefully fallback to an empty string (`""`).
* **Math Operations (`Calculate` tasks):** Implicitly fallback conversions (like treating a missing value as `0`) could lead to critical, silent calculation errors. Therefore, if a reference in a `Calculate` task points to something non-existent, the task immediately fails validation and fails the entire workflow safely.

### Supported Calculate Operations

| Operation | Aliases |
| :--- | :--- |
| Addition | `add`, `plus`, `+` |
| Subtraction | `subtract`, `minus`, `-` |
| Multiplication | `multiply`, `times`, `*` |
| Division | `divide`, `/`, `:` |
| Modulus | `mod`, `modulo`, `%` |

Division and modulus by zero, as well as operations producing non-finite results (NaN, Infinity), will cause the task to fail.

---

## HTTP API Specification

### 1. Submit a Workflow

**Endpoint:** `POST /workflows`

**Request Payload:**
```json
{
  "input": {
    "a": 10,
    "b": 5,
    "op": "divide"
  },
  "tasks": [
    {
      "type": "Calculate",
      "config": {
        "a": "$.input.a",
        "b": "$.input.b",
        "op": "$.input.op"
      }
    },
    {
      "type": "Print",
      "config": {
        "template": "Division result is: {{ $.steps.0 }}"
      }
    }
  ]
}
```

**Response (`201 Created`):**

```json
{
  "id": "e2f89ca9-2ab1-4603-911e-25ba835f8d68",
  "status": "Pending"
}
```

### 2. Get Workflow Status
**Endpoint:** `GET /workflows/{id}`

**Response (`200 OK`):**

```json
{
    "id": "e2f89ca9-2ab1-4603-911e-25ba835f8d68",
    "status": "Completed",
    "created_at": "2026-07-15T15:00:00Z",
    "updated_at": "2026-07-15T15:00:01Z",
    "tasks": [
        {
        "id": "3be587da-a2fc-4b53-96b6-527e58df7ca1",
        "type": "Calculate",
        "status": "Completed",
        "position": 0,
        "output": "2"
        },
        {
        "id": "5fcf309b-4bf1-479c-bfa8-0e3f885dfda2",
        "type": "Print",
        "status": "Completed",
        "position": 1,
        "output": "Division result is: 2"
        }
    ]
}
```

### 3. List Workflows (Paginated)
**Endpoint:** `GET /workflows?limit=10&offset=0`

**Response (`200 OK`):**

```json
[
    {
        "id": "e2f89ca9-2ab1-4603-911e-25ba835f8d68",
        "status": "Completed",
        "created_at": "2026-07-15T15:00:00Z",
        "updated_at": "2026-07-15T15:00:01Z"
    }
]
```

### How to Run
You can manage, test, and run the project easily using the provided Makefile.

### Quick Demo (Recommended!)
To see the workflow engine in action immediately with live, colorized status updates in your terminal:

1. In your first terminal window, spin up the engine in slow-motion mode (runs with a 1-second task execution delay so you can easily observe the worker concurrency limit):
```bash
make demo-up
```

2. In your second terminal window, run the interactive demo script:
```bash
make demo
```
The script will automatically submit multiple test workflows (successful runs and graceful failure scenarios) and poll their real-time execution status.

## Local Execution (Requires Go 1.25)
**Run tests:**
```bash
# Run the test suite
make test

# Run tests with the Go race detector (requires local C compiler / CGO_ENABLED=1)
make test-race

# Run tests with the race detector inside a Docker container (no local GCC/CGO needed)
make test-race-docker

# Run tests with coverage profiling
make test-cover

# Generate and open HTML coverage report
make cover-html
```

**Start the engine locally (creates a SQLite database in the `./data` directory):**
```bash
make run
```

## Containerized Execution (Docker Compose)
**Build and run the entire self-contained environment with a single command:**
```bash
make docker-up
```
The API will listen on http://localhost:8080.

The SQLite database is securely persisted within a named Docker volume.

To inspect runtime logs, use `make docker-logs`.

To tear down the container safely (preserving the database volume), use `make docker-down`.

To tear down the container and delete the database volume, use `make docker-down-volumes`.