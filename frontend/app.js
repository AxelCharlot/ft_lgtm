/**
 * The playground page: a Monaco editor, a Run button, and what the backend
 * answered.
 *
 * Monaco is served from vendor/ and never from a CDN. A CDN is a cloud service
 * and a dependency on the internet at run time, and the subject forbids both.
 * See plan.md section V.1.1.
 */

/**
 * Where the backend answers. The path is relative because the page and the API
 * share the host name lgtm.local, which keeps the browser same origin and
 * removes CORS. See k8s/README.md section 3.
 */
const RUN_ENDPOINT = '/api/run';

/** Where Monaco was vendored, relative to this page. */
const MONACO_DIRECTORY = 'vendor/monaco/';

/** The example the editor opens with. */
const EXAMPLE_PATH = 'example.rs';

/** Shown when the example cannot be read, so the editor is never empty. */
const FALLBACK_SOURCE = 'fn main() {\n    println!("hello");\n}\n';

/**
 * Said when the run worked and the upload did not.
 *
 * The contract calls this an empty cid with no error: the run is the result and
 * the sharing is a second thing that can fail on its own. So the sentence names
 * what still worked before what did not, and ends with the one action that helps.
 */
const NOT_SHARED =
    'The run worked, but it was not shared, so there is no link this time. ' +
    'Run it again to try once more.';

/** How long the copy button says it worked, in milliseconds. */
const COPIED_FOR = 1600;

/**
 * How each outcome is announced.
 *
 * The tone becomes a class on the output panel, and the panel carries a colour
 * for it. The subject asks a user to tell a compile error from a runtime error,
 * and reading is not the way: the two are different colours before they are
 * different words.
 *
 * Four tones, not seven, because the families are what matter. The compiler
 * refused the source; the program itself failed; the sandbox stopped it at a
 * limit; or something on our side broke and none of it is the user's doing.
 *
 * The kinds come from k8s/README.md section 4.
 *
 * @const {!Object<string, {label: string, tone: string}>}
 */
const OUTCOMES = {
  success: {label: 'done', tone: 'success'},
  compile: {label: 'compile error', tone: 'compile'},
  runtime: {label: 'runtime error', tone: 'runtime'},
  timeout: {label: 'stopped: out of time', tone: 'limit'},
  output_limit: {label: 'cut: too much output', tone: 'limit'},
  request: {label: 'the request was refused', tone: 'internal'},
  internal: {label: 'internal error', tone: 'internal'},
  unreachable: {label: 'the backend did not answer', tone: 'internal'},
};

/** Used when the backend sends a kind this page has never heard of. */
const UNKNOWN_OUTCOME = {label: 'unknown answer', tone: 'internal'};

let editor = null;

/**
 * Reads the example that ships beside this page.
 *
 * The source lives in its own file rather than in this one, so that rustfmt can
 * check it and a reader can open it as Rust.
 *
 * @return {!Promise<string>} the example, or a small program if it is missing.
 */
async function loadExample() {
  try {
    const response = await fetch(EXAMPLE_PATH);
    if (!response.ok) {
      throw new Error(`the server answered ${response.status}`);
    }
    return await response.text();
  } catch (error) {
    console.error('could not read the example:', error);
    return FALLBACK_SOURCE;
  }
}

/**
 * Copies text, on a page that is not allowed to use the modern way.
 *
 * The playground is served over plain HTTP on lgtm.local, and a browser gives
 * navigator.clipboard only to a secure context — so on this origin it is usually
 * absent. The textarea below is what still works there. It is checked for first
 * all the same, because the day this page is served over HTTPS the good path
 * should be the one that runs.
 *
 * @param {string} text what to put on the clipboard.
 * @return {!Promise<void>} rejected when the browser refused.
 */
function copyText(text) {
  if (window.isSecureContext && navigator.clipboard) {
    return navigator.clipboard.writeText(text);
  }

  return new Promise((resolve, reject) => {
    const holder = document.createElement('textarea');
    holder.value = text;
    holder.setAttribute('readonly', '');
    holder.style.position = 'fixed';
    holder.style.opacity = '0';
    document.body.appendChild(holder);
    holder.select();

    const copied = document.execCommand('copy');
    document.body.removeChild(holder);

    if (copied) {
      resolve();
    } else {
      reject(new Error('the browser refused to copy'));
    }
  });
}

/**
 * Hides both share rows. Every path through a run calls this before deciding
 * what to show, so a link from the run before can never survive into this one.
 */
function hideShare() {
  document.getElementById('share').hidden = true;
  document.getElementById('share-missing').hidden = true;
}

/**
 * Shows where the run was shared, or says that it was not.
 *
 * A run that failed is not shared at all, and no row appears: there is nothing
 * to link to and an explanation would only be noise under an error the user is
 * already reading.
 *
 * @param {{link: string, error: ?Object}} answer what the backend sent.
 */
function showShare(answer) {
  hideShare();

  if (answer.error) {
    return;
  }

  if (!answer.link) {
    const missing = document.getElementById('share-missing');
    missing.textContent = NOT_SHARED;
    missing.hidden = false;
    return;
  }

  const link = document.getElementById('share-link');
  link.href = answer.link;
  link.textContent = answer.link;
  document.getElementById('share').hidden = false;
}

/**
 * Announces an outcome: the colour of the panel, the words in the bar, and what
 * the program said.
 *
 * Every path through a run ends here, which is what makes the spinner reliable:
 * it is removed in one place rather than in five.
 *
 * @param {string} kind one of the keys of OUTCOMES, or `success`.
 * @param {string} body the text of the output panel.
 * @param {string=} reason a second line, under a rule, saying why it stopped.
 */
function showOutcome(kind, body, reason) {
  const outcome = OUTCOMES[kind] || UNKNOWN_OUTCOME;
  const panel = document.getElementById('output-panel');

  panel.dataset.tone = outcome.tone;
  panel.classList.remove('is-running');

  document.getElementById('status').textContent = outcome.label;
  document.getElementById('output').textContent = body;

  const reasonElement = document.getElementById('reason');
  reasonElement.textContent = reason || '';
  reasonElement.hidden = !reason;

  // showAnswer puts it back when there is something to share. Everything else
  // that ends a run — an unreachable backend, above all — leaves it hidden.
  hideShare();
}

/**
 * Says that a run is under way. The spinner belongs to this state alone.
 */
function showRunning() {
  const panel = document.getElementById('output-panel');
  panel.dataset.tone = 'running';
  panel.classList.add('is-running');

  document.getElementById('status').textContent = 'running';
  document.getElementById('output').textContent = '';
  document.getElementById('reason').hidden = true;
  hideShare();
}

/**
 * Shows one answer of the backend.
 *
 * A failed run still carries output: a panic prints its message before it stops,
 * and that message is the first thing the user looks for. So the output is shown
 * whenever there is any, and the reason goes under it.
 *
 * The link is a second result, not a part of the first: the run and the upload
 * fail on their own, and one failure must not hide the other result. So the
 * share row is decided after the outcome and never instead of it.
 *
 * @param {{output: string, cid: string, link: string,
 *          error: ?{kind: string, message: string}}} answer
 */
function showAnswer(answer) {
  const output = answer.output || '';

  if (!answer.error) {
    showOutcome('success', output);
    showShare(answer);
    return;
  }

  // A compile error has no output, and rustc's own text is the whole story, so
  // it becomes the body rather than a footnote under an empty panel.
  if (!output) {
    showOutcome(answer.error.kind, answer.error.message);
    return;
  }

  showOutcome(answer.error.kind, output, answer.error.message);
}

/**
 * Sends what the editor holds to the backend and shows the answer.
 *
 * @return {!Promise<void>}
 */
async function run() {
  const button = document.getElementById('run-button');
  button.disabled = true;
  showRunning();

  try {
    const response = await fetch(RUN_ENDPOINT, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({code: editor.getValue()}),
    });

    // The backend answers this shape on success and on failure alike, so the
    // body is read before the status is judged. See k8s/README.md section 4.
    const answer = await response.json();
    showAnswer(answer);
  } catch (error) {
    console.error('the run did not reach the backend:', error);
    showOutcome('unreachable', 'Nothing came back. The backend may be down.');
  } finally {
    button.disabled = false;
  }
}

/**
 * Builds the editor and wires the Run button.
 *
 * @param {string} source what the editor opens with.
 */
function start(source) {
  editor = monaco.editor.create(document.getElementById('editor'), {
    automaticLayout: true,
    fontSize: 13,
    language: 'rust',
    minimap: {enabled: false},
    scrollBeyondLastLine: false,
    tabSize: 4,
    theme: 'vs-dark',
    value: source,
  });

  const button = document.getElementById('run-button');
  button.disabled = false;
  button.addEventListener('click', run);

  const copy = document.getElementById('share-copy');
  copy.addEventListener('click', async () => {
    const link = document.getElementById('share-link').href;
    try {
      await copyText(link);
      copy.textContent = 'Copied';
    } catch (error) {
      // Saying "Copied" when nothing was copied is worse than saying nothing.
      // The link is selected instead, so one keystroke finishes the job.
      console.error('the link was not copied:', error);
      copy.textContent = 'Press Ctrl+C';
      window.getSelection().selectAllChildren(document.getElementById('share-link'));
    }
    setTimeout(() => {
      copy.textContent = 'Copy';
    }, COPIED_FOR);
  });

  document.getElementById('status').textContent = 'ready';
}

// baseUrl is what lets the editor find its worker, which lives next to it under
// vendor/. Both are on this origin, so no data URI and no CDN are involved.
window.MonacoEnvironment = {baseUrl: MONACO_DIRECTORY};

require.config({paths: {vs: `${MONACO_DIRECTORY}vs`}});
require(['vs/editor/editor.main'], async () => {
  start(await loadExample());
});
