import { hello } from "./leaf";

export function greet(): string {
  return hello() + " via mid";
}
