// Check annotations remain readable even when artifact/log downloads require
// authentication. Only emit these in a failed CI step.
import { existsSync, readFileSync } from 'node:fs';
const path = process.argv[2];
if (!path || !existsSync(path)) process.exit(0);
const escape = text => text.replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A');
const lines = readFileSync(path, 'utf8').split(/\r?\n/);
const failures = lines.filter(line => /error:|\berror\b|fatal|^--- FAIL:|^FAIL\b|\bfailed\b|panic:/i.test(line));
for (const line of failures.slice(-40)) console.log(`::error::${escape(line)}`);
