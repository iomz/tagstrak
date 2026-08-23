# Inventory files

`golemu server --file tags.json` loads and atomically updates a versioned JSON inventory.

```json
{"version":1,"tags":["3000","302db319a000004000000003"]}
```

Each tag is a lowercase hexadecimal EPC with an even byte length from 2 through 62 bytes.
EPC is inventory identity; LLRP Protocol Control is derived only while encoding reports.

`tagjson` imports legacy two-column CSV data into this format.
Its first PC column is ignored because it is an LLRP wire value, not domain identity.

Load limits cap total input at 16 MiB, records at 100,000, and EPCs at 62 bytes.
Invalid versions, duplicate EPCs, unknown fields, malformed JSON, and malformed EPCs are rejected.
Writes use same-directory temporary files, file sync, rename, and directory sync.
