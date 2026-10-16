---
title: "jaala"
description: "A Datalog engine for graph-shaped data, with provenance on every answer."
hideTitle: true
---

<div class="home-hero">
<h1>jaala</h1>
<p class="hero-subtitle">A Datalog engine for graph-shaped data. You describe what you want to know as rules over your facts, and every answer comes back with the facts that produced it.</p>
<div class="hero-actions">
<a href="{{.Site.PathPrefix}}/tutorials/" class="btn btn-primary">Start the tutorials</a>
<a href="{{.Site.PathPrefix}}/overview/" class="btn btn-secondary">What jaala is</a>
<a href="https://github.com/panyam/jaala" class="btn btn-outline">GitHub</a>
</div>
</div>

<div class="features">
<div class="feature-card">
<h3>Rules, recursion and negation</h3>
<p>Derived relations, recursion to a fixpoint, stratified negation and aggregation, in rule heads as well as in the answer.</p>
<a href="{{.Site.PathPrefix}}/overview/">Read the overview &rarr;</a>
</div>
<div class="feature-card">
<h3>Answers that cite their facts</h3>
<p>Every row carries the citations of the facts behind it, and on request a witness tree of the rules and facts that derived it.</p>
<a href="{{.Site.PathPrefix}}/overview/#provenance">How provenance works &rarr;</a>
</div>
<div class="feature-card">
<h3>Facts from your own stores</h3>
<p>A store answers only what a query binds, and can join, count, page and walk itself. A federation spreads one question over many stores.</p>
<a href="{{.Site.PathPrefix}}/guide/stores/">Facts from stores &rarr;</a>
</div>
<div class="feature-card">
<h3>Facts across versions</h3>
<p>One base over every revision: answer at one, across all of them with the versions each row holds in, or list what changed between two.</p>
<a href="{{.Site.PathPrefix}}/guide/versions/">Versions &rarr;</a>
</div>
<div class="feature-card">
<h3>A library, in Go or in the browser</h3>
<p>Pure Go with no dependencies outside the standard library, so it embeds in a service and runs in a browser as wasm.</p>
<a href="{{.Site.PathPrefix}}/overview/#three-packages">The three packages &rarr;</a>
</div>
</div>
