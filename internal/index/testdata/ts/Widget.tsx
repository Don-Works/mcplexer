import React from "react";
import { loadUser } from "./sample";

export default function Widget(): JSX.Element {
  return <div>widget</div>;
}

export const Panel = () => <section>panel</section>;

export class LegacyWidget extends React.Component {
  render() {
    return null;
  }
}

const lowercaseHelper = () => 42;
