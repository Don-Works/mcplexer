import { useState } from "react";
import type { Config } from "./config";
import { helper } from "./util/helper";
import defaultExport from "@/aliased/module";
const legacy = require("./legacy");

export type UserId = string;

export interface User {
  id: UserId;
  name: string;
}

export enum Role {
  Admin,
  Guest,
}

export function loadUser(id: UserId): User {
  return { id, name: "x" };
}

export const fetchUser = async (id: UserId): Promise<User> => {
  return loadUser(id);
};

export const MAX_USERS = 50;

function internalHelper(): void {
  useState();
}

const lazy = () => import("./lazy/mod");
export { Config } from "./config";
