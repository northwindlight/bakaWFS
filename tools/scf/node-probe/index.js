// Empty-workload comparison only; this is not an alternative bakaWFS backend.
'use strict';
const crypto = require('crypto');
const instance = crypto.randomBytes(20).toString('hex');
const initialized = new Date().toISOString();
let invocations = 0;

exports.main_handler = async event => {
  const started = process.hrtime.bigint();
  const sequence = ++invocations;
  const healthy = event.httpMethod === 'GET' && event.path === '/healthz';
  return {
    statusCode: healthy ? 200 : 404,
    headers: {
      'Content-Type': 'application/json',
      'Cache-Control': 'no-store',
      'X-BakaWFS-Instance': instance,
      'X-BakaWFS-Invocation': String(sequence),
      'X-BakaWFS-Initialized-At': initialized,
      'X-BakaWFS-Handler-Ms': (Number(process.hrtime.bigint() - started) / 1e6).toFixed(3),
    },
    body: healthy ? '{"status":"bootstrap"}' : '{"error":"not_found"}',
  };
};
