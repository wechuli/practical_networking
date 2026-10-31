# Fixture

- Directory: [tool](cmd/tool) and [data](data/), repository root: [repo](.)
- File: [`main.go`](cmd/tool/main.go), with a line: [line 2](data/lab.json#L2)
- Two on one line: [a](data/lab.json), [b](cmd/tool)
- Unchanged: [notes](docs/notes.md), [section](docs/notes.md#usage), [here](#fixture),
  [web](https://example.com/x.json), [mail](mailto:a@example.com), ![diagram](data/diagram.svg)
- Inline code is literal: `[tool](cmd/tool)` and ``[a](data/lab.json)``

```bash
cat [tool](cmd/tool)
```

````markdown
```
[nested](data/lab.json)
```
[still code](data/lab.json)
````

After the fence: [lab](data/lab.json)
