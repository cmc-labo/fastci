import { double } from "@myorg/utils";
import { triple } from "@myorg/utils/helpers";

export function run(): number {
  return double(triple(1));
}
