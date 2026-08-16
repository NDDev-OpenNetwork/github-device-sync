# ADR 0001: Local discovery and device snapshots

Status: Superseded by ADR 0018

Date: 2026-07-11

This ADR documented the pre-GDS container topology and its JSON device
snapshot. ADR 0018 replaced that model with typed portfolio-to-workspace
assignments in `estate/devices/*.yaml` and retired metadata repositories as
active Git or policy parents.

The legacy snapshot schema and implementation remain only as hermetic parity
fixtures until the legacy engine is removed. They are not desired-state or
runtime authorities.
