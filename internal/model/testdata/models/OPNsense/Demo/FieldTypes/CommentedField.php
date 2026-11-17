<?php
// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

namespace OPNsense\Demo\FieldTypes;

use OPNsense\Base\FieldTypes\TextField;

/* a commented-out override does not count: no custom-write */
class CommentedField extends TextField
{
    // public function setValue($value) { return parent::setValue(strtoupper($value)); }
    /*
    public function setValue($value)
    {
    }
    */
}
