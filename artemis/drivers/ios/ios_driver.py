"""Driver for an iPhone attached to the local Mac over USB."""

import asyncio
import base64
import json
import os
from pathlib import Path
from typing import Any

from artemis.drivers.base import BaseDeviceDriver, KeyCode, ScreenData, SwipeDirection
from third_party.mobile_use.utils.logger import get_logger

IOS_SERIAL_PREFIX = "ios:"
logger = get_logger(__name__)


class IOSDeviceDriver(BaseDeviceDriver):
    def __init__(self, device_id: str, width: int, height: int):
        self._device_id = device_id
        self._size = (width, height)
        self._helper = os.environ.get("ARTEMIS_IOS_HELPER", "artemis-ios")
        self.accessibility_enabled = os.environ.get("ARTEMIS_IOS_ACCESSIBILITY") == "1"

    @property
    def device_id(self) -> str:
        return self._device_id

    @property
    def screen_size(self) -> tuple[int, int]:
        return self._size

    async def _call(self, method: str, **params: Any) -> Any:
        try:
            process = await asyncio.create_subprocess_exec(
                self._helper,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
        except FileNotFoundError as exc:
            raise RuntimeError(
                "Install artemis-ios on PATH or set ARTEMIS_IOS_HELPER to the driver executable"
            ) from exc
        request = {
            "id": 1,
            "method": method,
            "device": self.device_id.removeprefix(IOS_SERIAL_PREFIX),
            **params,
        }
        try:
            stdout, stderr = await asyncio.wait_for(
                process.communicate((json.dumps(request) + "\n").encode()), timeout=40
            )
        finally:
            if process.returncode is None:
                try:
                    process.kill()
                except ProcessLookupError:
                    pass
                await process.wait()
        if process.returncode:
            raise RuntimeError(
                f"artemis-ios {method} failed: {stderr.decode(errors='replace').strip()}"
            )
        response = json.loads(stdout)
        if not isinstance(response, dict) or response.get("id") != 1:
            raise RuntimeError("artemis-ios returned an invalid response")
        if error := response.get("error"):
            raise RuntimeError(f"artemis-ios {method}: {error}")
        return response["result"]

    async def connect(self) -> None:
        devices = await self._call("list")
        for device in devices:
            if device["id"] == self.device_id.removeprefix(IOS_SERIAL_PREFIX):
                if device["state"] == "ready":
                    return
                raise RuntimeError(f"iPhone is not ready: {device['state']}")
        raise RuntimeError("Selected USB iPhone is not connected")

    @classmethod
    async def list_devices(cls) -> list[dict[str, str]]:
        return await cls("", 0, 0)._call("list")

    async def check_input(self) -> None:
        await self._call("check_input")

    async def disconnect(self) -> None:
        # Each helper invocation closes its device connection before exiting.
        return None

    async def get_accessibility_elements(
        self, *, screenshot_size: tuple[int, int] | None = None
    ) -> list[dict[str, Any]]:
        snapshot = await self._call("accessibility")
        if not isinstance(snapshot, dict) or not isinstance(snapshot.get("elements"), list):
            raise RuntimeError("artemis-ios returned an invalid accessibility snapshot")
        size = (snapshot.get("width"), snapshot.get("height"))
        if screenshot_size is not None and size != screenshot_size:
            raise RuntimeError("iOS accessibility bounds do not match the screenshot dimensions")
        return snapshot["elements"]

    async def get_screen_data(self, skip_settling: bool = False) -> ScreenData:
        shot = await self._call("screenshot")
        self._size = (shot["width"], shot["height"])
        elements = []
        if self.accessibility_enabled:
            try:
                elements = await self.get_accessibility_elements(screenshot_size=self._size)
            except (RuntimeError, ValueError, OSError, TimeoutError) as exc:
                logger.warning(f"iOS accessibility unavailable; continuing with screenshot: {exc}")
        data = ScreenData(
            screenshot_bytes=base64.b64decode(shot["png_base64"], validate=True),
            screenshot_base64=shot["png_base64"],
            width=shot["width"],
            height=shot["height"],
            platform="ios",
            ui_elements=elements,
        )
        return data

    async def tap(
        self, x: int, y: int, duration_ms: int = 100, times: int = 1, delay_ms: int = 100
    ) -> bool:
        if times != 1 or duration_ms != 100:
            raise NotImplementedError("iOS currently supports only a single standard tap")
        return await self._call("tap", x=x, y=y) is True

    async def launch_app(self, package_name: str) -> bool:
        return await self._call("launch", bundle_id=package_name) is True

    async def long_press(self, x: int, y: int, duration_ms: int = 1000) -> bool:
        raise NotImplementedError("iOS long press is not supported")

    async def swipe(
        self, start_x: int, start_y: int, end_x: int, end_y: int, duration_ms: int = 800
    ) -> bool:
        return (
            await self._call(
                "swipe", x=start_x, y=start_y, end_x=end_x, end_y=end_y, duration_ms=duration_ms
            )
            is True
        )

    async def swipe_direction(
        self, direction: SwipeDirection | str, duration_ms: int = 800
    ) -> bool:
        width, height = self.screen_size
        match SwipeDirection(direction):
            case SwipeDirection.UP:
                coordinates = (width // 2, 3 * height // 4, width // 2, height // 4)
            case SwipeDirection.DOWN:
                coordinates = (width // 2, height // 4, width // 2, 3 * height // 4)
            case SwipeDirection.LEFT:
                coordinates = (3 * width // 4, height // 2, width // 4, height // 2)
            case SwipeDirection.RIGHT:
                coordinates = (width // 4, height // 2, 3 * width // 4, height // 2)
        return await self.swipe(*coordinates, duration_ms=duration_ms)

    async def input_text(self, text: str, clear_existing: bool = True) -> bool:
        return await self._call("type", text=text, clear_existing=clear_existing) is True

    async def press_key(self, key: KeyCode | str | int) -> bool:
        raise NotImplementedError("iOS key events are not supported")

    async def stop_app(self, package_name: str) -> bool:
        raise NotImplementedError("Stopping an iOS app is not supported")

    async def get_current_package(self) -> str | None:
        return None

    async def execute_shell(self, command: str, timeout_seconds: float = 15.0) -> str:
        raise NotImplementedError("iOS devices do not expose an Artemis shell")

    async def start_video_recording(self, output_dir: Path | None = None) -> None:
        raise NotImplementedError("iOS video recording is not supported")

    async def stop_video_recording(self) -> str | None:
        raise NotImplementedError("iOS video recording is not supported")
