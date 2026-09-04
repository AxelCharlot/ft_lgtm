# Hostile code

Six programs, one per limit the subject asks for. Each one attacks the sandbox
from inside, and each one names the answer the backend must give. `run.sh` sends
them all and says whether the sandbox held.

```
vagrant ssh -c /vagrant/tests/hostile/run.sh
```

It exits non-zero if any program comes back with a kind other than the one it
expects. It ends by sending a program that works, because a sandbox that
survives an attack by refusing everything afterwards has not survived it.

The kinds come from [`k8s/README.md`](../../k8s/README.md) section 4.

## What each one does

| File | Attacks | Expected | Observed on 2026-08-29 |
|---|---|---|---|
| `endless_loop.rs` | the 5 s deadline | `timeout` | `timeout` — killed, no output |
| `huge_allocation.rs` | the 10 MiB memory limit | `runtime` | `runtime` — `memory allocation of 100000000 bytes failed` |
| `long_output.rs` | the 10 KiB output limit | `output_limit` | `output_limit` — 10 240 bytes kept, then the note counting the rest |
| `file_read.rs` | the file system | `runtime` | `runtime` — `Err(Os { code: 44, kind: NotFound })` |
| `network_connect.rs` | the network | `runtime` | `runtime` — `Err(Error { kind: Unsupported })` |
| `syntax_error.rs` | nothing; it does not compile | `compile` | `compile` — the full message of rustc |

All six, then a program that works, on the cluster of issue #20: every kind
matched, and `hello` came back after them.

## Two of these needed care

**The allocation is deleted without `black_box`.** Written the way plan.md writes
it, `rustc -O` sees that the length is the constant it was handed, removes the
allocation and prints the number. The run then succeeds having asked for no
memory at all, and the test measures the optimiser instead of the sandbox.
`memory.md` records that probe fooling us on 2026-08-22, and it fooled us again
on 2026-08-29 before this directory existed.

**A refused file and a refused socket do not fail on their own.** WASI answers
both with an ordinary `Err`, and a program that prints it exits zero — so the
run succeeds and nothing is visible anywhere. Both tests print the refusal, so
the exact reason stays in the output, and then unwrap it, so the trap arrives as
`runtime`.

## Every failure is visible — measured for #28 on 2026-09-01

`run.sh` was replayed against the full stack. All six were stopped the way they
must be, and all six appeared in Tempo and in Loki. The trace identifiers below
are from that replay. They are a record of what was read, not a link that stays
alive: the stores keep their data in the namespace, so `make clean` and any
rebuild of the machine replace them. Replay `run.sh` to get a fresh set.

| File | Trace | Spans below the root | Root status | Log line |
|---|---|---|---|---|
| `endless_loop.rs` | `b6cf233a72f73d8d3a886fad59dbdec7` | compile, execute | error — `the program ran past its deadline` | `the execute stage failed` |
| `huge_allocation.rs` | `f896e632b5fc151c4ead9ba8dad31955` | compile, execute | error — `the program stopped: unreachable` | `the execute stage failed` |
| `long_output.rs` | `f95a4eacf9b7726c3f17ae827839632d` | compile, execute | error — `the program printed more than the limit, and the rest was dropped` | `the execute stage failed` |
| `file_read.rs` | `fab840edf2b8f63075b18f7ef60a808a` | compile, execute | error — `the program stopped: unreachable` | `the execute stage failed` |
| `network_connect.rs` | `5b5eafdb0e5e368c0c665a7ec09d3a3e` | compile, execute | error — `the program stopped: unreachable` | `the execute stage failed` |
| `syntax_error.rs` | `a3dd8d12a7e27fecc3c5be88f797e147` | compile | error — `the source did not compile` | `the compile stage failed` |

`{ resource.service.name="lgtm-backend" && status=error }` returns exactly these
six and nothing else. The error rises from the failing stage to the root span, so
every one of them is red in the trace list, not only inside the waterfall.

Three details a reader meets here and nowhere else:

- **A failed run has no `upload` span and four log lines, not six.** Only a run
  that reached its end is shared, so the pipeline stops at the stage that failed.
- **Three failures carry the same message.** The memory limit, the refused file
  and the refused socket all arrive as `the program stopped: unreachable`, because
  all three are the same trap. The span attribute `code.sha256` is what tells them
  apart: `7e5663476a52ee22…`, `3437ae4ec3d4e5a8…` and `d57e2802731b237c…`.
- **Two of the six had no exemplar to click.** See the next section.

## An exemplar is one per bucket per 15 seconds

The six runs produced **four exemplars**, not six. `file_read.rs` and
`network_connect.rs` finished in 1.356 s and 1.377 s, in the same
`le="2"` bucket as `huge_allocation.rs` at 1.394 s, and inside one export cycle a
bucket keeps one exemplar. The first of the three survived; the other two were
dropped.

The cycle is what bounds it, not the run: `MetricInterval` in
`backend/internal/telemetry/telemetry.go` is 15 s, and two `endless_loop.rs` runs
27 s apart both kept their exemplar in the same `le="7.5"` bucket.

So a graph of exemplars is a sample of the runs, never a list of them. A panel
must not read an empty spot as a run that did not happen, and #30 must not send a
reader looking for a run by clicking. Tempo search by `status=error` finds every
run; exemplars find some of them.

## One thing worth knowing when you read the answers

`timings.execute_ms` is `0` on every failure, including the endless loop that
burned its full five seconds. The pipeline fills that field only when the
program ran to the end. It is not wrong about the run, only silent about it, and
#17 is where the timing of a request gets rebuilt.
