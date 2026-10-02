General rules:
==============
 - If a task has a "blocked" label, it must be resolved by the operator (human) before it can proceed.
 - If a task is blocked by a dependency, it cannot proceed until the dependency is resolved.
 - If a task is assigned to the operator (human), it should only be acted upon by the operator.
 - If a task is assigned to another agent, it should only be acted upon by that agent.

Events and rules:
=================

# Implementation

## An assigned task is added to the board
When a task is added to the board and it is already assigned to an agent, the dispatcher notifies the assigned agent of the new task. The agent is responsible for acting on the task accordingly, regardless of the column the task is in (eg: if the task is in the "Todo" or "In Progress" column, the agent should start or continue working on it; if it is in the "In Review" column, the agent should review it.).

## An unassigned task is added to the board
The dispatcher selects an appropriate agent to assign the task to, based on the workload. Once an agent is selected, the dispatcher assigns the task to that agent and notifies them of the new assignment. Regardless of the column the task is in, the agent is responsible for acting on the task accordingly (eg: if the task is in the "Todo" or "In Progress" column, the agent should start or continue working on it; if it is in the "In Review" column, the agent should review it.).

# Pull Requests

## A review is requested for a pull request:
The dispatcher notifies the agent assigned as the reviewer with the pull request that requires a review. The agent should immediately start reviewing the pull request and provide feedback or approval as necessary.

## A re-review is requested for a pull request:
The dispatcher notifies the agent who received the re-review request with the pull request that requires a re-review. The agent should immediately start reviewing the pull request and provide feedback or approval as necessary. If the author has accepted and implemented requested changes since the last review, the agent should take those changes into account during the re-review.

If the author has rejected requested changes, the agent should validate the reasons for the rejection. If the reasons are valid, the agent should accept the rejection; if no reasons are given or the reasons are not valid, it should provide feedback explaining why the rejection is not valid and add a "blocked" label to the pull request and to the task linked to the pull request (if any).

## Changes are requested for a pull request:
The agent should review the requested changes, verify the requested changes are valid and act accordingly.

If the changes are valid, the agent should apply requested changes to the pull request; otherwise, the agent should provide feedback why it cannot be applied. Finally it should request a re-review from the reviewer.
