#!/usr/bin/env node
'use strict';

const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
const LOG_DIR = path.join(ROOT, 'logs');

function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 1) {
    const item = argv[i];
    if (!item.startsWith('--')) continue;
    const key = item.slice(2);
    const next = argv[i + 1];
    if (next && !next.startsWith('--')) {
      args[key] = next;
      i += 1;
    } else {
      args[key] = true;
    }
  }
  return args;
}

function normalizeBaseUrl(value) {
  return String(value || '').trim().replace(/\/+$/, '');
}

function managementUrl(baseUrl, pathname) {
  const normalized = normalizeBaseUrl(baseUrl);
  if (!normalized) throw new Error('missing CLIProxyAPI base URL');
  if (/\/v0\/management$/i.test(normalized)) return `${normalized}${pathname}`;
  return `${normalized}/v0/management${pathname}`;
}

function isInvalidated401Auth(file) {
  const message = String(file?.status_message || '');
  return /Your authentication token has been invalidated|auth_unavailable|HTTP 401|\b401\b/i.test(message);
}

function safeLogName(date = new Date()) {
  return date.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
}

async function requestJson(method, url, managementKey) {
  const response = await fetch(url, {
    method,
    headers: {
      Authorization: `Bearer ${managementKey}`,
      'X-Management-Key': managementKey,
      Accept: 'application/json',
    },
  });
  const text = await response.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch (_) { data = text; }
  }
  if (!response.ok) {
    const detail = typeof data === 'string' ? data : JSON.stringify(data);
    throw new Error(`${method} ${url} failed: HTTP ${response.status}${detail ? ` ${detail}` : ''}`);
  }
  return data;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const baseUrl = normalizeBaseUrl(
    args['base-url'] ||
    process.env.CLIPROXYAPI_BASE_URL ||
    process.env.CLI_PROXY_API_BASE_URL ||
    'http://127.0.0.1:8317'
  );
  const managementKey = String(
    args.key ||
    args['management-key'] ||
    process.env.CLIPROXYAPI_MANAGEMENT_KEY ||
    process.env.CLI_PROXY_API_MANAGEMENT_KEY ||
    ''
  ).trim();

  if (!managementKey) throw new Error('missing management key: set CLIPROXYAPI_MANAGEMENT_KEY or pass --key');

  const listed = await requestJson('GET', managementUrl(baseUrl, '/auth-files'), managementKey);
  const files = Array.isArray(listed?.files) ? listed.files : [];
  const targets = files.filter(isInvalidated401Auth);
  const deleted = [];
  const failed = [];

  for (const file of targets) {
    const name = String(file.name || file.id || '').trim();
    if (!name) {
      failed.push({ name: '', email: file.email || file.account || '', error: 'missing auth file name' });
      continue;
    }
    const url = `${managementUrl(baseUrl, '/auth-files')}?name=${encodeURIComponent(name)}`;
    try {
      await requestJson('DELETE', url, managementKey);
      deleted.push({ name, email: file.email || file.account || '', status: file.status || '' });
    } catch (error) {
      failed.push({ name, email: file.email || file.account || '', error: error.message || String(error) });
    }
  }

  const result = {
    ok: failed.length === 0,
    checkedAt: new Date().toISOString(),
    baseUrl,
    scanned: files.length,
    matched401: targets.length,
    deleted: deleted.length,
    failed: failed.length,
    deletedItems: deleted,
    failures: failed,
  };

  fs.mkdirSync(LOG_DIR, { recursive: true });
  fs.writeFileSync(path.join(LOG_DIR, 'cleanup-auth-401-latest.json'), `${JSON.stringify(result, null, 2)}\n`, 'utf8');
  fs.writeFileSync(path.join(LOG_DIR, `cleanup-auth-401-${safeLogName()}.json`), `${JSON.stringify(result, null, 2)}\n`, 'utf8');
  console.log(JSON.stringify(result, null, 2));
  if (failed.length > 0) process.exitCode = 1;
}

main().catch((error) => {
  console.error(error.stack || error.message || String(error));
  process.exit(1);
});
