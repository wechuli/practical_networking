# Fixture

- Directory: [tool](https://github.com/example/repo/tree/0123abc/cmd/tool) and [data](https://github.com/example/repo/tree/0123abc/data), repository root: [repo](https://github.com/example/repo/tree/0123abc)
- File: [`main.go`](https://github.com/example/repo/blob/0123abc/cmd/tool/main.go), with a line: [line 2](https://github.com/example/repo/blob/0123abc/data/lab.json#L2)
- Two on one line: [a](https://github.com/example/repo/blob/0123abc/data/lab.json), [b](https://github.com/example/repo/tree/0123abc/cmd/tool)
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

After the fence: [lab](https://github.com/example/repo/blob/0123abc/data/lab.json)
