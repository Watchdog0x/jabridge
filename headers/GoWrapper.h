#ifndef WRAPPER_H
#define WRAPPER_H

#include "Common.h"

extern void firstScanForDevicesDone(void);

extern void deviceAttachedFunc(Jabra_DeviceInfo deviceInfo);

extern void deviceRemovedFunc(uint16_t deviceID);

// extern void buttonInDataRawHidFunc(unsigned short deviceID, unsigned short usagePage, unsigned short usage, unsigned char buttonInData);

// extern void buttonInDataTranslatedFunc(unsigned short deviceID, Jabra_HidInput translatedInData, bool buttonInData);

// extern void batteryStatusUpdate(unsigned short deviceID, Jabra_BatteryStatus* batteryStatus);

#endif