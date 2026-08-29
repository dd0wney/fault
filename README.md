# fault

Walk the point of failure through every operation your code performs.

```go
for n, p := range fault.Sweep(t) {
	fs := faultfs.New(p)

	err := store.OpenAndWrite(fs)

	if !reopens(t, dir) {
		t.Errorf("op %d: the store did not reopen: %v", n, err)
	}
}
```

The loop fails the first operation, then the second, then the third, and it
stops when the fault runs off the end of the sequence. Every error path the
scenario can reach has then been visited.

## Why

A single fault test picks one point in the sequence, and picks it by luck. The
interesting points sit inside a rename, between a write and its sync, and on
the close after a failed sync. Nobody guesses those.

SQLite has run this loop for twenty years. Go has no equivalent.

## Layout

| Package | What it does |
|---|---|
| `fault` | Counts operations and reports which one must fail. No dependencies. |
| `fault/fs` | Filesystem adapter. |
| `fault/alloc` | Allocator adapter. |
| `fault/tools` | Test-instrument gates. A separate module, and not part of the library. |

## Status

Early. The API is not stable.

## License

BSD-3-Clause.
