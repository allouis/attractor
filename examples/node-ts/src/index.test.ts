import { test } from "node:test";
import assert from "node:assert";
import { add, greeting } from "./index";

test("add sums two numbers", () => {
  assert.strictEqual(add(2, 3), 5);
});

test("greeting formats a name", () => {
  assert.strictEqual(greeting("attractor"), "hello, attractor");
});
