<?php
// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

namespace OPNsense\Demo\FieldTypes;

use OPNsense\Base\FieldTypes\TextField;
use OPNsense\Base\FieldTypes\ArrayField;
use OPNsense\Base\FieldTypes\ContainerField;

/* stores base64, like OPNsense\Auth\FieldTypes\StoreB64Field */
class StoredField extends TextField
{
    public function setValue($value)
    {
        return parent::setValue(base64_encode($value));
    }
}
