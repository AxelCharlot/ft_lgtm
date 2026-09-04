# Monaco, vendored

Version **0.52.2**, from `npm install monaco-editor@0.52.2`, copied out of
`node_modules/monaco-editor/min/vs`.

It is here rather than on a CDN because the subject forbids a cloud service and
forbids a dependency on the internet at run time. A CDN is both.

## What was kept, and what was not

The full `min/vs` weighs 14 MB. This holds 4.3 MB.

| Kept | Why |
|---|---|
| `loader.js` | the AMD loader the page calls |
| `editor/editor.main.js`, `editor/editor.main.css` | the editor itself |
| `base/worker/workerMain.js` | the editor worker |
| `base/browser/ui/codicons/codicon/codicon.ttf` | the icon font |
| `basic-languages/rust/rust.js` | the only language this playground offers |

| Removed | Size | Why |
|---|---|---|
| `language/` | 7 MB | the language services for TypeScript, JSON, HTML and CSS. Rust is not one of them: it lives in `basic-languages/` |
| the other `basic-languages/` | 963 kB | one language is offered, and it is Rust |
| `nls.messages.*.js` | 1.6 MB | nine interface translations. English is inside `editor.main.js` |

## To raise the version

```
docker run --rm -v "$PWD:/w" -w /w node:22-alpine \
  npm install --no-fund --no-audit monaco-editor@<version>
```

Then copy the files of the first table out of `node_modules/monaco-editor/min/vs`
and check the page again with no network: `basic-languages/rust/rust.js` must
still load, and the editor must still colour the example.

The licence beside this file is Monaco's own, and it stays.
