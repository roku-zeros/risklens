# Synthetic audit fixture

The current `Evaluate()` function selects a product branch. This statement still
requires reading the conditions; symbol existence alone does not prove it.

[Current source](main.go#L1-L10)

The following references are deliberately stale and must be reported:

- `legacy/limit.go:999`
- `MissingLimitBuilder()`

`Application.Limit` is a promoted field; its declaration belongs to `Decision`.
