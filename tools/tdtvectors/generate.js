/*
 * generate.js builds committed reference vectors with the pinned GS1 TDT
 * reference translator.
 *
 * This is a development-time oracle, not the normative authority: where a
 * reference vector disagrees with a vector transcribed from the TDS text, the
 * spec vector wins and the disagreement is a bug report. The generated file is
 * committed, so neither CI nor the runtime needs Node.
 *
 *   node tools/tdtvectors/generate.js
 *
 * Output: internal/tdt/testdata/vectors/reference.json
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const root = path.resolve(__dirname, '..', '..');
const artefactDir = path.join(root, 'internal', 'tdt', 'tdtdef', 'artefacts');
const translatorPath = path.join(__dirname, 'vendor', 'TDTtranslator.js');
const outPath = path.join(root, 'internal', 'tdt', 'testdata', 'vectors', 'reference.json');
const schemes = ['SGTIN-96', 'SSCC-96', 'GIAI-96', 'GRAI-96'];
const filters = [0, 1, 3, 7];
const MAX_EXACT = BigInt(Number.MAX_SAFE_INTEGER);

function loadTranslator() {
  const source = fs.readFileSync(translatorPath, 'utf8');
  const sandbox = {
    console: { debug() {}, log() {}, warn() {}, error() {} },
    // The translator fetches its artefacts in a browser. Here the data is
    // injected directly, so fetch must exist but is never expected to succeed.
    fetch: () => Promise.reject(new Error('offline')),
    JSZip: {},
    module: { exports: {} },
  };
  sandbox.globalThis = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(source + '\nmodule.exports = { TDTtranslator };', sandbox, { filename: translatorPath });
  return sandbox.module.exports.TDTtranslator;
}

function loadData() {
  const data = { scheme: {}, table: {} };
  for (const name of schemes) {
    data.scheme[name] = JSON.parse(fs.readFileSync(path.join(artefactDir, name + '.json'), 'utf8'));
  }
  return data;
}

function levelOf(artefact, type) {
  return artefact['tdt:epcTagDataTranslation'].scheme.level.find((l) => l.type === type);
}

function pad(value, field) {
  if (field.length === 0) {
    // A field the standard fixes at zero length, such as GRAI-96's asset type
    // when the company prefix takes all twelve digits.
    return '';
  }
  const s = String(value);
  return field.length > 0 ? s.padStart(field.length, '0') : s;
}

// candidates returns the boundary values worth exercising for one field.
//
// The bound is the smaller of the pure identity maximum and the binary field
// maximum. They differ: GIAI-96's pure identity allows a thirteen digit asset
// reference while the binary field holds 42 bits, and TDS requires a longer
// encoding rather than an overflowing one. The reference translator does not
// enforce that, which is one reason it is an oracle and not the authority.
function candidates(field, binaryMax) {
  let max = BigInt(field.decimalMaximum);
  if (binaryMax !== undefined && binaryMax < max) {
    max = binaryMax;
  }
  // The reference translator handles field values as JavaScript numbers, so it
  // loses precision above 2^53-1 and encodes neighbouring values. Vectors are
  // capped below that so the oracle stays trustworthy; the ranges beyond it are
  // covered by the property and fuzz tests instead.
  if (max > MAX_EXACT) {
    max = MAX_EXACT;
  }
  const values = [0n, 1n, max];
  if (max > 2n) {
    values.push(max / 2n);
  }
  return [...new Set(values.map((v) => v.toString()))];
}

function main() {
  const TDTtranslator = loadTranslator();
  const translator = new TDTtranslator();
  if (translator.initialized && translator.initialized.catch) {
    translator.initialized.catch(() => {});
  }
  translator.tdtData = loadData();

  const rows = [];
  const rejected = [];
  for (const name of schemes) {
    const artefact = translator.tdtData.scheme[name];
    const pure = levelOf(artefact, 'PURE_IDENTITY');
    const binary = levelOf(artefact, 'BINARY');
    for (const option of pure.option) {
      const gcpLength = Number(option.optionKey);
      const binaryOption = binary.option.find((o) => o.optionKey === option.optionKey);
      const binaryMax = {};
      for (const f of binaryOption.field) {
        binaryMax[f.name] = BigInt(f.decimalMaximum);
      }
      const base = option.field.map((f) => pad(candidates(f, binaryMax[f.name])[0], f));
      const variants = [];
      // One vector per field boundary value, plus an all-maximum vector.
      option.field.forEach((f, i) => {
        for (const value of candidates(f, binaryMax[f.name])) {
          const fields = base.slice();
          fields[i] = pad(value, f);
          variants.push(fields);
        }
      });
      variants.push(option.field.map((f) => pad(candidates(f, binaryMax[f.name])[2], f)));

      let filterIndex = 0;
      for (const fields of variants) {
        const filter = filters[filterIndex % filters.length];
        filterIndex += 1;
        const tagURI = `urn:epc:tag:${name.toLowerCase()}:${filter}.${fields.join('.')}`;
        const options = { filter: String(filter), tagLength: String(artefact['tdt:epcTagDataTranslation'].scheme.tagLength) };
        const hex = translator.translate(tagURI, name, 'HEX', options);
        const pureURI = translator.translate(tagURI, name, 'PURE_IDENTITY', options);
        if (!hex || !pureURI) {
          throw new Error('translator returned nothing for ' + tagURI);
        }
        const expectedHexLength = artefact['tdt:epcTagDataTranslation'].scheme.tagLength / 4;
        if (String(hex).length !== expectedHexLength) {
          // The reference translator sometimes emits more bits than the scheme
          // holds. A 96-bit scheme cannot produce a longer encoding, so the
          // row is recorded as a divergence rather than committed as a vector.
          rejected.push({ tagURI, hex: String(hex).toLowerCase(), reason: 'encoded length exceeds tag length' });
          continue;
        }
        rows.push({
          scheme: name.toLowerCase(),
          gcpLength,
          filter,
          tagURI,
          pureIdentity: String(pureURI),
          hex: String(hex).toLowerCase(),
          provenance: 'reference',
        });
      }
    }
  }
  const seen = new Set();
  const unique = rows.filter((r) => {
    const key = r.scheme + '|' + r.tagURI;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  rows.length = 0;
  rows.push(...unique);
  rows.sort((a, b) => (a.scheme + a.hex + a.filter).localeCompare(b.scheme + b.hex + b.filter));
  const out = {
    source: 'gs1/TDT demo/TDTtranslator.js, pinned in tools/tdtvectors/vendor',
    note: 'Reference oracle, not the normative authority. Where these disagree with vectors transcribed from the TDS text, the spec vectors win.',
    rejected,
    vectors: rows,
  };
  fs.writeFileSync(outPath, JSON.stringify(out, null, '\t') + '\n');
  process.stdout.write(`tdtvectors: wrote ${rows.length} reference vectors (${rejected.length} reference divergences recorded) to ${path.relative(root, outPath)}\n`);
}

main();
