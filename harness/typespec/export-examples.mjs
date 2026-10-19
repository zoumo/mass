import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import {
  compile,
  formatDiagnostic,
  getExamples,
  NodeHost,
  serializeValueAsJson,
} from "@typespec/compiler";

const check = process.argv.includes("--check");
const input = fileURLToPath(new URL("../modules/runtime/event-examples.tsp", import.meta.url));
const output = new URL("../modules/runtime/event-examples.json", import.meta.url);
const program = await compile(NodeHost, input, { noEmit: true, warningAsError: true });
if (program.diagnostics.length > 0) {
  for (const diagnostic of program.diagnostics) {
    process.stderr.write(`${formatDiagnostic(diagnostic)}\n`);
  }
  process.exit(1);
}

const domain = program.getGlobalNamespaceType().namespaces.get("MASS").namespaces.get("Design");
const model = domain.namespaces.get("Examples").models.get("SessionEventExamples");
const examples = getExamples(program, model);
if (examples.length !== 1) throw new Error("Expected one SessionEventExamples example.");
const preview = serializeValueAsJson(program, examples[0].value, model);

const expected = new Set([...domain.enums.get("SessionEventType").members.values()].map((member) => member.value));
const ids = new Set();
for (const event of preview.events) {
  if (!expected.delete(event.type)) throw new Error(`Unexpected or duplicate sample type: ${event.type}`);
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(event.eventId)) {
    throw new Error(`Invalid sample UUID: ${event.eventId}`);
  }
  if (ids.has(event.eventId)) throw new Error("Sample event UUIDs must be unique.");
  ids.add(event.eventId);
  if ("seq" in event || "_meta" in event.payload || "messageCompleted" in event.payload) {
    throw new Error(`Obsolete sample field: ${event.type}`);
  }
}
if (expected.size > 0) throw new Error(`Missing sample types: ${[...expected].join(", ")}`);

const contentTypes = new Set(["text", "image", "audio", "resource", "resource_link"]);
for (const block of preview.contentBlocks) contentTypes.delete(block.type);
if (contentTypes.size > 0) throw new Error(`Missing content examples: ${[...contentTypes].join(", ")}`);

const serialized = `${JSON.stringify(preview, null, 2)}\n`;
if (check) {
  if (await readFile(output, "utf8") !== serialized) {
    throw new Error("JSON preview is stale. Run npm run examples --prefix harness/typespec.");
  }
} else {
  await writeFile(output, serialized);
}
process.stdout.write(`${check ? "Checked" : "Generated"} ${preview.events.length} event examples.\n`);
