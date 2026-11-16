<?php
// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

namespace OPNsense\Demo\FieldTypes;

use OPNsense\Base\FieldTypes\ArrayField;
use OPNsense\Base\FieldTypes\ContainerField;

/* two classes in one file, as OPNsense\Interfaces\FieldTypes\NetworkInterfaceField */
class LegacyEntryContainerField extends ContainerField
{
    public function toLegacy($sanitize = true)
    {
        return [];
    }

    public function fromLegacy($data)
    {
    }
}

class LegacyEntryField extends ArrayField
{
    public function newContainerField($ref, $tagname)
    {
        $node = new LegacyEntryContainerField($ref, $tagname);
        return $node;
    }
}

/* an array field without own storage code */
class PlainEntryField extends ArrayField
{
}
