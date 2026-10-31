import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";

function filesUnder(directory) {
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name.startsWith(".") || entry.name === "__pycache__") return [];
    const target = path.join(directory, entry.name);
    return entry.isDirectory() ? filesUnder(target) : [target];
  });
}

const requested = process.argv.slice(2);
const documents = requested.length
  ? requested
  : [
      "networking_course_outline.md",
      "appendices.md",
      "implementation.md",
      "improvements.md",
      ...filesUnder("modules").filter((file) => file.endsWith(".md")),
      ...filesUnder("labs").filter((file) => file.endsWith(".md")),
    ];
let links = 0;
let embeddedPython = 0;
for (const file of documents) {
  const text = fs.readFileSync(file, "utf8");
  const pythonBlocks = [...text.matchAll(/```python\n([\s\S]*?)```/g)].map(
    (match) => match[1],
  );
  const heredocs = [
    ...text.matchAll(/<<\s*['"]?(PYEOF|PYTHON|PY)['"]?\s*\n([\s\S]*?)\n\1\b/g),
  ].map((match) => match[2]);
  const commandStrings = [...text.matchAll(/python3 -c "\n([\s\S]*?)\n"/g)].map(
    (match) => match[1],
  );
  for (const source of [...pythonBlocks, ...heredocs, ...commandStrings]) {
    const result = spawnSync(
      "python3",
      ["-c", "import ast,sys; ast.parse(sys.stdin.read())"],
      {
        input: source,
        encoding: "utf8",
        timeout: 10000,
      },
    );
    assert.equal(
      result.status,
      0,
      `${file}: embedded Python: ${result.stderr}`,
    );
    embeddedPython++;
  }
  let fence = null;
  for (const line of text.split("\n")) {
    const match = line.match(/^\s*(`{3,}|~{3,})(.*)$/);
    if (match) {
      if (fence === null) fence = match[1];
      else if (
        match[1][0] === fence[0] &&
        match[1].length >= fence.length &&
        !match[2].trim()
      )
        fence = null;
      continue;
    }
    if (fence) continue;
    for (const reference of line.matchAll(/\]\(([^)\s]+)\)/g)) {
      if (/^(https?:|mailto:|#)/.test(reference[1])) continue;
      const [relative, anchor] = decodeURIComponent(reference[1]).split("#");
      const target = path.resolve(path.dirname(file), relative);
      assert.ok(fs.existsSync(target), `${file}: missing link ${relative}`);
      if (anchor && /^L\d+$/.test(anchor)) {
        const count = fs.readFileSync(target, "utf8").split("\n").length;
        assert.ok(
          Number(anchor.slice(1)) > 0 && Number(anchor.slice(1)) <= count,
          `${file}: invalid line anchor ${reference[1]}`,
        );
      }
      links++;
    }
  }
  assert.equal(fence, null, `${file}: unclosed code fence`);
}

let executables = 0;
for (const file of filesUnder("labs")) {
  let result;
  if (file.endsWith(".sh")) {
    result = spawnSync("bash", ["-n", file], {
      encoding: "utf8",
      timeout: 10000,
    });
    const source = fs.readFileSync(file, "utf8");
    assert.ok(
      !/--flood|--rand-source|nf_conntrack_max\s*=/.test(source),
      `${file}: forbidden unbounded traffic or shared conntrack tuning`,
    );
  } else if (file.endsWith(".py")) {
    result = spawnSync(
      "python3",
      ["-c", "import ast, sys; ast.parse(sys.stdin.read())"],
      {
        input: fs.readFileSync(file, "utf8"),
        encoding: "utf8",
        timeout: 10000,
      },
    );
  } else if (file.endsWith(".json")) {
    JSON.parse(fs.readFileSync(file, "utf8"));
    continue;
  } else continue;
  assert.equal(result.status, 0, `${file}: ${result.error || result.stderr}`);
  executables++;
}
// Go labs: formatting, vet, and the loopback-only test suite. These run real
// sockets on 127.0.0.1 but need no root, network namespaces, or internet.
const goModule = "labs/go";
let goPackages = 0;
if (requested.length === 0 && fs.existsSync(path.join(goModule, "go.mod"))) {
  const go = (args) =>
    spawnSync("go", args, { cwd: goModule, encoding: "utf8", timeout: 600000 });
  const version = go(["version"]);
  assert.ok(
    !version.error,
    "Go is required for labs/go; install Go 1.24+ (see Module 0)",
  );
  const unformatted = spawnSync("gofmt", ["-l", "."], {
    cwd: goModule,
    encoding: "utf8",
  });
  assert.equal(unformatted.stdout.trim(), "", `gofmt needed:\n${unformatted.stdout}`);
  for (const args of [["vet", "./..."], ["test", "-race", "-count=1", "./..."]]) {
    const result = go(args);
    assert.equal(result.status, 0, `go ${args.join(" ")}:\n${result.stdout}${result.stderr}`);
  }
  goPackages = go(["list", "./..."]).stdout.trim().split("\n").filter(Boolean).length;
}

console.log(
  `PASS: ${documents.length} documents, ${links} local links, ${executables} executable syntax checks`,
);
if (goPackages) console.log(`PASS: ${goPackages} Go package(s) formatted, vetted, and tested`);
console.log(`PASS: ${embeddedPython} embedded Python blocks/heredocs compiled`);
console.log(
  "Static validation does not execute privileged Linux, container, or cloud labs.",
);
