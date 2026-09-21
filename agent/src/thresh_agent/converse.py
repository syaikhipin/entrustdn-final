"""The agent's conversation loop over Channels.

Ticket 01's smoke path: open a conversation by saying hello. Later tickets
grow this into the LangGraph pause-for-days/resume loop with quality
triggers; the seam (Channel) stays the same.
"""

import asyncio

from .channels import Channel, ChannelMessage, DeliveryAck


def open_conversation(channel: Channel, opening: ChannelMessage) -> DeliveryAck:
    """Send the opening message of a conversation over a Channel."""
    return asyncio.run(channel.deliver(opening))
