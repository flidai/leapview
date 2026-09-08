# FAI-592 canonical Watermill envelope — superseded

Status: superseded by FAI-720 and FAI-721.

LeapView retains the canonical PostgreSQL event record but no framework
envelope. Product mutations append that record in their caller-owned
transaction. There is no consumer registry, delivery fan-out, broker metadata,
or transport offset in the current target.

A future asynchronous consumer must define its own typed input and idempotent
effect before any transport adapter is selected.
