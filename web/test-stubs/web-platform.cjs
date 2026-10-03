const { TextDecoder, TextEncoder } = require('node:util');
const { ReadableStream } = require('node:stream/web');

Object.assign(globalThis, { TextDecoder, TextEncoder, ReadableStream });
