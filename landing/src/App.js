import React from "react";
import logo from "./logo.png";
import "./App.scss";
import JoinOrStart from "./JoinOrStart.js";

function App() {
  return (
    <div className="App" style={{ backgroundColor: "#006e9bff" }}>
      <div className="app-content">
        <img src={logo} className="App-logo" alt="logo" />
        <h2>Start a room. Join a room. Play. Too easy!</h2>
        <div className="divider" />
        <JoinOrStart />
      </div>
    </div>
  );
}

export default App;
