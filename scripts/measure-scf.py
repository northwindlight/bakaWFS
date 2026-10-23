#!/usr/bin/env python3
"""Measure Function URL probes with connection reuse and auditable JSON output."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import http.client
import json
import math
from pathlib import Path
import time
from urllib.parse import urlsplit


def request(connection, target, label, phase):
    started = time.perf_counter()
    try:
        connection.request('GET', target, headers={'Cache-Control': 'no-cache'})
        response = connection.getresponse()
        body = response.read()
        elapsed = (time.perf_counter() - started) * 1000
        valid = response.status == 200 and json.loads(body) == {'status': 'bootstrap'}
        return {'label': label, 'phase': phase, 'status': response.status, 'valid': valid,
                'end_to_end_ms': round(elapsed, 3), 'instance': response.getheader('X-BakaWFS-Instance'),
                'invocation': response.getheader('X-BakaWFS-Invocation'),
                'handler_ms': response.getheader('X-BakaWFS-Handler-Ms')}
    except Exception as error:
        return {'label': label, 'phase': phase, 'valid': False, 'error_type': type(error).__name__}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go-url', required=True)
    parser.add_argument('--node-url', required=True)
    parser.add_argument('--samples', type=int, default=30)
    parser.add_argument('--burst', type=int, default=8)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if not 1 <= args.samples <= 1000 or not 1 <= args.burst <= 32:
        parser.error('samples must be 1..1000 and burst 1..32')
    endpoints = []
    for label, url in [('go', args.go_url), ('node', args.node_url)]:
        parsed = urlsplit(url)
        if parsed.scheme != 'https' or not parsed.hostname or parsed.query or parsed.username or parsed.password:
            parser.error('provide an HTTPS probe URL without credentials or query')
        endpoints.append((label, parsed.hostname, parsed.port or 443, parsed.path or '/healthz'))
    records = []
    connections = {label: http.client.HTTPSConnection(host, port, timeout=30) for label, host, port, _ in endpoints}
    try:
        for index in range(args.samples + 1):
            # Alternate order. The first network connection is separated from reused connections.
            for label, _, _, target in endpoints[::1 if index % 2 == 0 else -1]:
                records.append(request(connections[label], target, label, 'connection_setup' if index == 0 else 'sequential'))
        for label, host, port, target in endpoints:
            def single(_):
                connection = http.client.HTTPSConnection(host, port, timeout=30)
                try:
                    return request(connection, target, label, 'burst_new_connection')
                finally:
                    connection.close()
            with ThreadPoolExecutor(max_workers=args.burst) as pool:
                records.extend(pool.map(single, range(args.burst)))
    finally:
        for connection in connections.values():
            connection.close()
    summaries = {}
    for label, _, _, _ in endpoints:
        samples = sorted(r['end_to_end_ms'] for r in records if r['label'] == label and r['phase'] == 'sequential' and r['valid'])
        summaries[label] = {'successful_sequential': len(samples),
                            'instances_observed': len({r['instance'] for r in records if r['label'] == label and r['valid']}),
                            'failures': sum(not r['valid'] for r in records if r['label'] == label)}
        for percentile in (50, 95, 99):
            summaries[label]['p' + str(percentile) + '_ms'] = samples[max(0, math.ceil(len(samples) * percentile / 100) - 1)] if samples else None
    report = {'timestamp_utc': datetime.now(timezone.utc).isoformat(),
              'note': 'Sequence 1 identifies first handler invocation, not a platform-confirmed cold start. Verify Init Report Coldstart in platform logs. Sequential samples attempt HTTPS connection reuse; burst includes new TLS connections.',
              'summary': summaries, 'samples': records}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(summaries, ensure_ascii=False, indent=2))
    if any(not record['valid'] for record in records):
        raise SystemExit(1)


if __name__ == '__main__':
    main()
