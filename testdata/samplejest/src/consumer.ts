import { greet } from "@app/mid";

export function run(): string {
  return greet() + " via consumer";
}
