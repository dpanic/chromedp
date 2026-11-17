# Attach to a target without Runtime.enable on request

Status: Proposed.

This change adds the context option `WithoutRuntimeEnable`. The maintainer has
not decided on it yet.

## Why

The first `Run` of a context attaches the target and sends `Runtime.enable`.
While the Runtime domain is enabled, Chrome serializes each argument of a
console call for the protocol. A page can see this work. For example, it logs
an error with `console.debug`, and the serialization formats the stack of the
error. Then `Error.prepareStackTrace` runs, or a getter on the name of the
error runs once more. A page that does not run under the protocol sees
neither. So any page can tell that a program drives it.

The command is not needed for most actions. `Runtime.evaluate` and
`Runtime.callFunctionOn` work without it. The queries and `Poll` need it only
for the execution context of each frame, which the target learns from the
`Runtime.executionContextCreated` events.

## What was done

- `WithoutRuntimeEnable() ContextOption` makes `attachTarget` leave out
  `Runtime.enable`. The default does not change. Without the option, the
  target sends the same commands in the same order as before, and every
  existing test passes unchanged.
- Without `Runtime.enable`, `frameExecContext` gets the execution context of
  a frame from `Page.createIsolatedWorld`, on the first query or poll in the
  frame. The target keeps the world for each frame. It drops the world when
  the frame navigates or detaches, and drops every world when the document is
  updated, because a new document has none of the old worlds.
- A reply of `Page.createIsolatedWorld` that arrives after a drop can name a
  world of the old document. The target uses it for that call, and does not
  keep it.
- When Chrome cannot create a world, the loops of the queries and of `Poll`
  send the command again after 200 ms, and not every 5 ms. When the context
  of the action ends, the target logs the last error through the error log
  func. The action returns the error of its context, as before.
- The worker check of `attachTarget` uses `Runtime.evaluate`, so it works
  without `Runtime.enable`.

## The limits of the option

- Chrome sends no events of the Runtime domain. `Console` gets no console API
  calls and no uncaught exceptions, but still gets the entries of the browser
  log. `Listen`, `Events` and `WithListener` get no Runtime events.
- Chrome does not add a binding of `Runtime.addBinding` to the page. So
  `ExposeFunc` returns no error, but the page gets no `window.<name>`.
- The predicate of `Poll` and `PollFunction`, and the execution context that a
  func of `WaitFunc` gets, are in an isolated world. They see the DOM, and
  not the JavaScript globals of the page. `Evaluate` runs in the main world,
  as before.

## What was not done

- `ExposeFunc` does not return an error on a context with the option. It
  returns no error, and the page gets no func. An error is a small change,
  and it can come later if the maintainer wants it.
- The option is not inherited by a child context, as `WithListener` is not.
