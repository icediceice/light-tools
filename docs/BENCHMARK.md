# Benchmark: what reaches the model

**Generated file — do not edit by hand.** Regenerate with:

```sh
go test -tags treesitter ./internal/bench/ -run TestBenchmarkReport -update
```

Every number below is produced by `internal/bench` on a fixed corpus with no network, no clock and no randomness, so the same checkout always yields the same table.

## What is measured

**Delivered bytes** — how much text has to be put in front of a model to answer a fixed question, and **calls** — how many round trips that takes.

This is *not* a measure of task success, wall time, or answer quality. It cannot tell you an agent solved anything faster. It measures context cost and nothing else.

Bytes, not tokens: no tokenizer is involved. Bytes and tokens are not proportional across content types — repetitive log output compresses into relatively fewer tokens than prose does, so a byte ratio probably *understates* the log-track difference. The conservative direction is the right one for a published number.

## The three arms

| Arm | What it does | Calls |
| --- | --- | --- |
| `native-naive` | Returns the whole thing: the entire file, the entire stream. | 1 |
| `native-skilled` | Greps first, then reads a bounded window around the hits. | 2+ |
| `light-tools` | The real shipped handlers, with the options the tools actually pass. | 1 |

**Quote the headline against `native-skilled`, not `native-naive`.** The naive arm is real behaviour — it is what an agent does on first contact with an unfamiliar corpus — but a competent agent does better, and comparing only against the weaker baseline would be marketing.

### The rule that keeps the skilled baseline fair

A grep is only as good as the pattern you could think of *before* you had the answer. So the skilled arm's pattern is derived from the **question**, never from the **answer**. Searching a restart log for `restart` is fair; searching it for `614` is not, because that is the answer and assuming it turns the baseline into a lookup of a fact it already had.

Applied honestly, this rule costs light-tools rows. Where a question names the string it is looking for, a skilled grep is extremely efficient and wins. Those rows are in the table. The conclusion to draw is not *light always delivers less* — it is that grep requires you to already know what you are looking for, and an outline does not.

### Answer preservation

Delivering fewer bytes is trivial if you may delete the answer. Every scenario carries a regexp matching the fact that answers its question, checked against what each arm actually delivered:

- If the **light** arm loses the answer, the test suite **fails**. That claim does not ship.
- If a **baseline** arm loses it, the row is marked ✗ and quotes **no ratio** — a baseline is never credited for bytes it saved by not answering the question.

### Reading the marks

| Mark | Meaning |
| --- | --- |
| † | **Adversarial row.** Chosen because light-tools is *not* expected to win it. |
| ✗ | That arm did **not** deliver the answer. No ratio is quoted against it. |
| ‡ | **Repeat read.** Measures a second look at unchanged bytes, not first contact. |
| **lost** | **light-tools did not answer this row.** A known limitation, kept in the table. |

## Log reading

| Scenario | Corpus | naive | skilled | light | light vs skilled | Calls (n/s/l) |
| --- | ---: | ---: | ---: | ---: | ---: | :---: |
| `access-log-500s` | 2.0 MB | 2.0 MB | 732 B | 570 B ✗ | **lost** | 1/2/1 |
| `build-failure` | 13.7 KB | 13.7 KB | 13 B ✗ | 358 B | n/a | 1/2/1 |
| `restart-loop` | 339.9 KB | 339.9 KB | 1.8 KB | 606 B | 3.0× | 1/2/1 |
| `short-status` † | 640 B | 640 B | 78 B | 640 B | 0.12× (light 8.2× larger) | 1/2/1 |
| `test-failure` | 25.0 KB | 25.0 KB | 111 B | 2.8 KB | 0.04× (light 25.7× larger) | 1/2/1 |

**Against the skilled baseline: light-tools delivered less in 2 of 5 rows, more in 2, and failed to answer 1.**

Round trips are counted separately and are not in that tally — several rows where light-tools delivers *more* bytes still answer in one call where the baseline needs two.

**`access-log-500s`** — Something is throwing 500s. Which endpoint is it?

> SYNTHETIC — 20,000 access-log lines, two of them 500s on the same path
>
> THE ROW light-tools LOSES, and the most informative one here. All 20,000 lines share a shape, so they collapse into a single template group whose variable slots are summarised INDEPENDENTLY: the view reports that a 500 occurred and that six distinct paths exist, but the correlation between those two slots is gone. It can tell you a 500 happened; it cannot tell you which endpoint. A skilled grep answers this outright and in fewer bytes. Template collapse summarises what VARIES, so it is strong on repetition and weak on correlating two rare values across slots — and this is what that weakness looks like. The exact lines remain recoverable through the spill pointer the view carries.
>
> **light-tools does not answer this one.** The compacted view is smaller, and smaller is worth nothing here because it does not contain the answer. A skilled grep wins this row outright. The exact lines stay recoverable through the spill pointer the view carries, but that costs another call and the reader has to know to make it.

**`build-failure`** — The build failed. Which file broke, and what was the error?

> SYNTHETIC — 500 uniform compile-progress lines, one compiler diagnostic, one verdict
>
> The baseline greps 'error|FAILED|fail' — the obvious pattern from the question. Go's compiler diagnostic contains none of those words, so this row is expected to show a baseline MISS. That is the point of keeping it: the pattern was reasonable and it still missed.
>
> **The skilled baseline did not find the answer here.** Its pattern was a reasonable guess from the question and it still missed, which is why no ratio is quoted for this row.

**`restart-loop`** — A service is flapping. Is it restart-looping, and how far has the restart counter climbed?

> SYNTHETIC — 3,616 lines of systemd/kernel boot chatter with a 16-restart loop buried inside it
>
> The case exact-duplicate deduplication cannot touch: no two counter lines are byte-identical, so a dedup pass collapses nothing and the loop stays buried. Grouping by shape is what surfaces it.

**`short-status`** — Is light-edge running, and since when?

> SYNTHETIC — a 13-line systemctl status block
>
> ADVERSARIAL. This sits under the pass-through floor (internal/logs/analyze.go: defaultMinLines 40 AND defaultMinBytes 4000), so light-tools must hand it back byte-for-byte and this row must report parity. A saving here would mean compaction had started touching output that was already legible. The row is a canary, not filler.

**`test-failure`** — Did the suite pass? If not, which test failed and why?

> SYNTHETIC — a verbose Go test run, 360 passes and one failure

## Code reading

| Scenario | Corpus | naive | skilled | light | light vs skilled | Calls (n/s/l) |
| --- | ---: | ---: | ---: | ---: | ---: | :---: |
| `batch-across-files` | 5.2 KB | 5.2 KB | 794 B | 748 B | 1.1× | 3/6/1 |
| `repeat-read-unchanged` ‡ | 19.3 KB | 19.3 KB | 19.3 KB | 109 B | 181.7× | 1/1/1 |
| `symbol-in-large-file` | 19.3 KB | 19.3 KB | 1.6 KB | 704 B | 2.4× | 1/2/1 |
| `symbol-in-medium-file` | 1.8 KB | 1.8 KB | 270 B | 523 B | 0.52× (light 1.9× larger) | 1/2/1 |
| `tiny-file` † | 270 B | 270 B | 207 B | 358 B | 0.58× (light 1.7× larger) | 1/2/1 |

**Against the skilled baseline: light-tools delivered less in 3 of 5 rows, more in 2, and failed to answer 0.**

Round trips are counted separately and are not in that tally — several rows where light-tools delivers *more* bytes still answer in one call where the baseline needs two.

**`batch-across-files`** — Show me ResolveLedger, ResolveTransport and ResolveRegistry — I need all three before I can change any of them.

> SYNTHETIC — three Go files, one wanted declaration in each
>
> The row where ROUND TRIPS, not bytes, are the native path's real cost: three files means three reads, and a grep-first agent pays six. The light arm asks once.

**`repeat-read-unchanged`** — I read this file earlier and need to confirm it has not changed.

> SYNTHETIC — the ~700-line file, read twice with no edit between
>
> The light arm returns a dedup stub rather than the content, which is correct ONLY because the reader already holds it from the first read. Scored accordingly: the first read must have carried the answer and the stub must identify the exact bytes it stands for. This row measures a repeat, so it does not generalise to first contact.

**`symbol-in-large-file`** — I need to change Coordinator.ReconcileExpiredSessions. Show me its current body.

> SYNTHETIC — a ~700-line Go file holding 61 declarations
>
> The everyday case for an editing agent: the symbol is known, the file is not small, and only the declaration is wanted.

**`symbol-in-medium-file`** — Show me ResolveTransport so I can change its error path.

> SYNTHETIC — a ~70-line Go file holding 8 declarations
>
> A moderate file. Included so the track is not built only from the extreme case — a suite of nothing but 700-line files would overstate the everyday result.

**`tiny-file`** — What does UserAgent return?

> SYNTHETIC — an 11-line Go file
>
> ADVERSARIAL. Reading the whole file was already the right call, so extraction has almost nothing to remove and the JSON envelope around the result may well make the light arm LARGER than the naive one. A negative row here is the suite working.

## Limitations

Read these before quoting anything above.

1. **The corpora are synthetic.** They are generated by committed code (`corpus.go`, `code_fixtures.go`) with shapes drawn from real systemd, compiler, access-log and test output — not captured from a production host. Real logs carry hostnames and paths that do not belong in a public repository, and a multi-megabyte capture makes the repo worse for everyone who clones it. **This is the largest weakness here:** a generator written by the party who benefits from the result can flatter it. The mitigations are partial — the generators are short enough to audit, and the suite includes rows the tool loses. Someone who does not trust a synthetic corpus should run the same arms over their own logs; they are exported for that.

2. **The baselines are modelled, not executed.** No real `cat` or `grep` subprocess runs. The models are simple and deliberately generous to the baseline.

3. **The naive arm is uncapped.** It delivers the whole corpus. An agent whose shell tool truncates at some limit delivers less than the figure shown — though truncation also silently drops whatever fell outside the cut, often the verdict at the end. Rather than invent a specific competitor's cap, the corpus size is printed so you can apply your own.

4. **This measures delivered context, not task success.** No model was run. Nothing here shows an agent solved a task faster or better — only that less had to be put in front of it.

5. **The code track assumes the symbol is already known.** It measures extraction, not search. light-tools is not a code-intelligence layer and is not credited as one.

6. **Absolute paths are normalised.** `filetool` returns absolute paths, which would make byte counts vary by temp directory. The fixture root is replaced with `<root>` before counting, identically in every arm.

7. **No aggregate across tracks is published.** The two tracks measure different mechanisms — repetition collapse and targeted extraction — and one blended figure would be an average of incomparable things.

## What this measurement found against us

Template collapse summarises each variable slot **independently**, so the correlation between two slots is lost. When 20,000 access-log lines share one shape, the view correctly reports that a `500` occurred and that several distinct paths exist — but not *which path* returned the 500. The `access-log-500s` row is marked **lost** for exactly this reason, and a skilled `grep` beats it there on both counts.

The general shape of it: compaction is strong where the signal is **repetition** and weak where the signal is **a rare correlation between two fields**. If you already know the string you are looking for, grep for it — that is not a concession, it is the correct tool. What compaction gives you is the case where you *cannot* name the string yet, because you do not know what is in the log.

This section exists because a benchmark that never reports against its own tool is not measuring anything.
