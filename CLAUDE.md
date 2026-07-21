## Project Overview


## Testing

- Framework: Ginkgo v2 + Gomega with gomock.
- Run unit tests with `abt t u [dir]` (wraps `ginkgo -r --skip Integration`). Use `go test ./path/... -v -run "<name>"` only for targeted single-test runs.
- Before marking any coding task complete, run `abt t lint` and resolve everything it reports. This runs `go fmt`, `go mod tidy`, misspell, `go vet`, and `staticcheck`.
- No stray `FDescribe`/`FIt`/`FContext` focus prefixes in committed tests.

## Working protocol

- **Exploratory questions** ("how could we…", "what do you think about…"): propose an approach and wait for explicit confirmation before implementing. Do not start work on the assumption that exploration implies approval.
- **Non-trivial plans** must be reviewed before presenting to the user:
  1. Draft the plan.
  2. Spawn three `Agent` calls in parallel: one security-and-consistency reviewer, one adversarial reviewer, one UX reviewer.
  3. Incorporate suggestions that hold up.
  4. Present the final plan to the user with a short "reviewer input adopted" section so the user can see what shifted.
  5. If you have questions, ask them in the review. But before continuing, always give the user a chance to ask 
     questions or steer the plan as a final step. Just because he answered your questions does not mean you are 
     ready to move on. Ask for final user input.
- **Suspected bugs in existing code**: do not write tests that lock in behavior you suspect is wrong. Stop, describe the concern, ask the user how to proceed.

## Code style

- License header: Apache-2.0 with Choria copyright. Match existing files.
- Do not add comments like ```// ----- ask_human_confirm: a yes/no question -----``` which is followed by a function doing exactly that, just dont add comments of this form at all
- We use American English - specialize not specialise.
- When adding dependencies use the latest, dont add v1 or a package if a newer is present
- No emojis, no emdashes, no unicode characters unless absolutely needed
- Import grouping: stdlib, blank line, external packages, blank line, internal packages.
- Error wrapping: `fmt.Errorf("%w: %w", ErrOuter, err)`.
- Structured logging with key-value pairs.
- No emojis in code, tests, or documentation unless the user explicitly asks.
- We avoid code like `_ := foo()` that only exist to keep linters happy but have no value.
- Expand compound `if` statements: prefer

  ```go
  x, err := thing()
  if err != nil {
      return err
  }
  if x == 1 {
      ...
  }
  ```

  over `if x, err := thing(); err == nil && x == 1 { ... }`.

## Do not, without asking first

- Add new top-level packages.
- Add, remove, or upgrade external dependencies (including Go toolchain version).
- Change public APIs outside the scope of the requested task.
- Modify `ABTaskFile`, `Dockerfile.goreleaser`, or CI configuration.
- Run destructive git operations (force-push, reset --hard, branch -D) or skip hooks.
- Change any git configuration
- Do not perform `find` operations over the entire OS `/`
