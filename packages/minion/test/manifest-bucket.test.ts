import test from 'node:test';
import assert from 'node:assert/strict';
import { withSelectedBucket, manifestDigest, type AccessManifest } from '../src/manifest.js';

/** Caller-selected routing participates in the exact artifact digest, while omission preserves legacy consent. */
test('selected bucket is explicit, bounded and digest-bound', () => {
 const manifest: AccessManifest = { schema_version: 1, name: 'demo', version: '1.0.0', services: { example: { version: 'v1', operations: ['Read'] } } };
 assert.equal(withSelectedBucket(manifest), manifest);
 assert.equal(withSelectedBucket(manifest, 'demo').bucket, 'demo');
 assert.notEqual(manifestDigest(withSelectedBucket(manifest, 'demo')), manifestDigest(manifest));
 // Invalid selections must not become an omitted bucket or enter artifact bytes as another data type.
 for (const value of ['', '*', ' demo', 'demo\n', 'x'.repeat(256), {}]) assert.throws(() => withSelectedBucket(manifest, value as string));
});
