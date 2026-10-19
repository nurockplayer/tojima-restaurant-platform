#!/usr/bin/env node
// Re-apply the acceptance criteria (lib/evaluate.mjs) to an existing report without re-capturing.
//   node reevaluate.mjs <outDir> <before|after>
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { evaluateReport, renderSummary } from './lib/evaluate.mjs';
const [dir, label] = process.argv.slice(2);
if (!dir || !label) { console.error('usage: node reevaluate.mjs <outDir> <before|after>'); process.exit(2); }
const file = join(dir, `report-${label}.json`);
const report = JSON.parse(readFileSync(file, 'utf8'));
const UI = { name: label };
report.evaluation = evaluateReport(report, UI);
writeFileSync(file, JSON.stringify(report, null, 2));
writeFileSync(join(dir, `summary-${label}.md`), renderSummary(report, UI));
console.log(`re-evaluated ${file}`);
